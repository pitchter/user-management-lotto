# Design decisions and assumptions

What was decided, and why. Where a choice has a real cost, the cost is stated
too.

---

## Assumptions made about the brief

The challenge leaves a few things open. These are the readings taken, so a
reviewer can see where the implementation went beyond the literal text.

| Question | Decision |
| --- | --- |
| "Create a new user" vs. registration — one endpoint or two? | Two. `POST /auth/register` is public and always produces a `user`; `POST /users` is admin-only and can set the role. A single endpoint would either let anyone mint an admin or make admins unable to. |
| Who may call the user operations? | RBAC with `admin` and `user`. An `admin` manages everyone; a `user` manages only itself. The brief says to protect the endpoints but not with what granularity; "any valid token may do anything" seemed too weak for a service whose whole subject is users. |
| Does "update a user's name or email" mean both at once? | Either or both, as a `PATCH` with optional fields. Sending neither is an error rather than a silent no-op. |
| May a user delete their own account? | No. Deletion is admin-only, and an admin cannot delete itself (see below). |
| Should listing be paginated? | Yes. An endpoint that returns every row is fine in a test and a problem in production. |

---

## Architecture

### Hexagonal, with the dependency rule enforced by the linter

The ports (`UserRepository`, `PasswordHasher`, `TokenService`, `Clock`) are
declared in `internal/domain`, the layer that needs them, and implemented in
`internal/adapter`. `internal/domain` imports nothing but the standard library.

What this buys, concretely:

- The gRPC server and the REST handlers call the same `UserService`. The
  permission rules exist once. Adding a third transport would add no logic.
- The use-case tests run against an in-memory repository, so the whole unit
  suite finishes in seconds with no database and no Docker.
- Swapping MongoDB for PostgreSQL would touch one package.

The cost is indirection: reading one request end to end means walking through
handler → DTO → service → port → adapter. For a service this small that is a
real price, and it is paid deliberately — the brief names hexagonal
architecture as a goal, and the layering is what makes the mocking story
honest rather than ceremonial.

Because an architecture nobody enforces decays, `depguard` in
[`.golangci.yml`](../.golangci.yml) fails the build if `domain` or `app` ever
imports gin, the MongoDB driver, `golang-jwt` or gRPC.

### Permissions live in the application layer, with the HTTP layer as a front gate

`RequireAdmin` middleware marks admin-only routes, which makes the route table
in `router.go` self-documenting and rejects unauthorised requests before they
reach the database. The authoritative check is still in `UserService`, because
gRPC has no middleware chain and must not be able to bypass a rule. Defence in
depth, and no rule that exists only in a transport.

### `User.ID` is a `string` in the domain

The domain holds the hex form; the conversion to and from `bson.ObjectID`
happens in `internal/adapter/mongodb/model.go`. The alternative — putting
`bson.ObjectID` in the entity — would mean the core knows about MongoDB, which
is exactly what the architecture is meant to prevent.

### `userDoc` is separate from `domain.User`

Database field names can change, and storage-only fields can be added, without
touching the entity. It also keeps `bson` tags out of the domain.

### No transport tags anywhere inside the domain or the application layer

The same rule as above, applied consistently: no type in `internal/domain` or
`internal/app` carries a `json` or `bson` tag. `app.UserOutput` has none, and
neither does `domain.FieldError`; each adapter declares its own wire type
(`httpapi.userResponse`, `httpapi.fieldDetail`, `mongodb.userDoc`) and maps
onto it.

That costs a mapping function per type. What it buys is that the shape of the
JSON is decided in exactly one place, so renaming a wire field cannot silently
reach into a business rule, and the two protocols can disagree about
presentation without the core knowing.

### Why the token rejection reason is logged but never returned

`jwtauth.Verify` says precisely what went wrong — expired, bad signature, wrong
audience. The caller gets none of it, only `the access token is invalid or has
expired`, because the difference between "expired" and "wrong signature" is
information an attacker can use. The reason goes to the server log as a
`token_rejected` line carrying the request id, which is where it is actually
useful. REST and gRPC do this identically.

---

## Security

### The same answer for "no such email" and "wrong password"

`POST /auth/login` returns the identical body, status and timing in both cases.
The timing part matters: when the address is unknown the service still runs a
bcrypt comparison against a dummy hash, so the request costs what a real one
costs. Returning 401 immediately would let an attacker time the responses and
enumerate registered addresses.

`DummyHash()` is part of the `PasswordHasher` port rather than a constant in
the application layer, because the right value depends on the algorithm the
adapter chose.

### HS256 pinned at verification time

`jwt.WithValidMethods([]string{"HS256"})` is passed to the parser, so the `alg`
header that arrives with a token is never consulted to decide how to verify it.
Without that pin, two classic attacks work: `alg: none` (a token with no
signature at all) and algorithm confusion (switching to an asymmetric
algorithm and offering the public key as the HMAC key).
`internal/adapter/jwtauth/token_test.go` asserts that both are rejected, for
HS512, HS384, RS256, ES256 and assorted casings of `none`.

### The secret must be at least 32 bytes, checked at start-up

RFC 7518 section 3.2 requires an HMAC key at least as long as the hash
function's output, which for HS256 (HMAC-SHA-256) is 32 bytes. A shorter key
weakens the signature and brings a brute-force search within reach. The process
refuses to boot on a short or missing secret rather than running insecurely —
and it validates every other setting in the same pass, so a misconfiguration is
one error message, not five restarts.

### Tokens carry no profile data

Only `sub`, `role` and the registered claims. A JWT payload is base64, readable
by anyone holding the token, and copies of `name`/`email` go stale the moment
the user edits them.

### Passwords

bcrypt at cost 12 by default, configurable between 10 and 15. Passwords over
72 bytes are **rejected** rather than truncated: bcrypt only reads the first 72
bytes, so silently accepting a longer one would make two different passwords
interchangeable. The policy check lives in the domain, so it applies to
sign-up, admin-created users and the bootstrap admin alike.

The password policy is minimal on purpose — a length floor and a byte ceiling.
Composition rules ("one uppercase, one symbol") are now advised against by
NIST SP 800-63B, and anything more serious (breach-list checks, rate limiting,
MFA) is a separate piece of work rather than a half-measure here.

### `UserOutput` has no password field at all

Not a `json:"-"` tag — the field does not exist in the type. It therefore
cannot leak through any serialisation path: JSON, protobuf or a log line.

### Logs redact credentials at the handler

Anything named `password`, `password_hash`, `authorization`, `token`,
`jwt_secret`, `api_key`, `cookie` (case-insensitively) is replaced with
`[REDACTED]` by the slog handler, which is the last stop before bytes are
written. A future careless `slog.Any("req", req)` cannot leak a password.

### 403 before 404

For an ordinary user, asking for another user's id returns 403 whether or not
that id exists. Checking existence first would turn the status code into an
account-enumeration oracle.

### Internal errors are opaque

An unrecognised error becomes a 500 with "an internal error occurred". The real
message — which may name a collection, a host or a failing query — goes to the
server log with the request id. Tests assert that a simulated MongoDB failure
leaks none of that through either REST or gRPC.

### An admin cannot delete its own account

If the last admin deleted itself, nobody could create or edit users again;
recovery would mean setting `BOOTSTRAP_ADMIN_*` and restarting. A guard is
cheaper than that footgun. The alternative — counting the remaining admins and
allowing self-deletion when others exist — adds a query and a race for little
benefit.

### Other hardening

`ReadHeaderTimeout` (Slowloris), a 1 MiB request-body cap enforced at the
reader rather than after parsing, no trusted proxies by default (so a forged
`X-Forwarded-For` cannot misattribute a log line), `Cache-Control: no-store` on
token responses, gRPC keepalive enforcement, a non-root distroless image with
all capabilities dropped.

---

## Data model and storage

### A unique index on `email`, plus normalisation

Addresses are stored lower-cased and trimmed, and a unique index enforces
uniqueness in the database. Both halves matter:

- normalising means `A@b.com` and `a@b.com` are one account, without relying
  on a MongoDB collation;
- the index means two simultaneous sign-ups cannot both succeed. Checking
  "is this address free?" and then writing leaves a window between the two
  steps, so the code does not do that — it writes, and translates a duplicate
  key error into `ErrEmailTaken`. There is an integration test that fires eight
  concurrent sign-ups at one address and asserts exactly one wins.

### Keyset pagination, not offset

`GET /users` filters on `_id > cursor` sorted by `_id`, rather than using
`skip`. Offset pagination has two problems: MongoDB must walk past every
skipped document, so deep pages get slower, and the window shifts when someone
signs up mid-paging, which duplicates or drops records.

The trade-off: a client cannot jump to "page 7". Nothing in the brief needs
that, and `total` is still returned for display.

### `$set` of the mutable fields, not a whole-document replace

`Update` writes `name`, `email`, `password_hash`, `role` and `updated_at`.
`_id` and `created_at` are never touched, and a field added in future is not
silently dropped by an older build.

### `CountDocuments`, not `EstimatedDocumentCount`

The estimate reads collection metadata and can drift. The background job
reports a number a reviewer will compare against reality, so it counts.

---

## Testing

### MongoDB is mocked at the port, not at the driver

Four reasons, in order of weight:

1. **`mtest` is not importable.** In mongo-driver v2 it moved under
   `internal/`, so mocking at the driver level is not available at all. (Most
   tutorials showing `mtest` are written against v1.)
2. **The ports are narrow.** `UserRepository` has seven methods, so an
   in-memory fake is short, readable, and can reproduce the behaviour that
   matters — unique emails, sortable ids, keyset paging.
3. **A fake that really works lets tests assert on outcomes.** With generated
   mocks, a test tends to assert that methods were called in a certain order,
   which pins the test to today's implementation and breaks on every refactor.
   Here a test can say "after this call the repository holds one user with this
   email" — which is what we actually care about.
4. **No generation step.** Clone the repository and `go test ./...` works.

The catch is that a fake can drift from the real adapter, which is exactly why
the adapter has its own integration tests against a real MongoDB 8 in a
container (`make test-integration`): CRUD, the unique index under concurrent
writes, bson type conversion, keyset paging, and connection failure handling.
They sit behind the `integration` build tag so the everyday suite stays fast
and Docker-free.

### `testing/synctest` for the 10-second worker

The background job's period is the requirement, so it has to be tested. With
real time that means waiting 30+ seconds for three ticks, and still being
flaky. `testing/synctest` (standard library, Go 1.25+) gives the goroutines in
its block virtual time: `time.Sleep` returns at once while the ticker believes
the time passed. The test asserts the exact number of reports in a fraction of
a second, deterministically — and synctest also fails the test if a goroutine
is left running, which makes it a leak check too.

### Tests use the real router and the real service

The HTTP tests drive the actual gin router, wired to the actual `UserService`,
backed by the fake repository. Stubbing at the service boundary instead would
let the suite pass with the permission rules broken, which would make the tests
worse than none at all for the thing most worth protecting.

---

## Operational choices

### Graceful shutdown

`signal.NotifyContext` catches SIGINT and SIGTERM; an `errgroup` runs the HTTP
server, the gRPC server and the worker. On a signal, both servers stop
accepting connections and wait for in-flight work, in parallel so the combined
wait cannot exceed `SHUTDOWN_TIMEOUT`, and MongoDB is disconnected last. If the
timeout expires, gRPC is forced down so the process cannot hang forever.

The shutdown path uses a context detached from the cancelled one — otherwise
`Shutdown` would return immediately and cut off the very requests it is meant
to protect.

### The bootstrap admin

RBAC has a chicken-and-egg problem: only an admin can create an admin. With
`BOOTSTRAP_ADMIN_EMAIL` and `BOOTSTRAP_ADMIN_PASSWORD` set, the first admin is
created at start-up, idempotently — if that address already exists nothing is
touched, including the password, so restarts never overwrite real data or
silently reset a changed password.

Setting one of the pair without the other is a configuration error rather than
a silent skip, because it is almost always a mistake.

### The binary health-checks itself

The image is distroless: no shell, no curl, no wget. Rather than give that up,
`/api healthcheck` performs the probe itself, and `HEALTHCHECK` in the
Dockerfile calls it. The image stays small with a minimal attack surface and
still has a working health check.

### One error envelope, with a request id

Every error — including 404s, 405s and recovered panics — uses the same shape,
so a client needs one error handler. `code` is the contract; `message` is prose
that may be reworded. The `request_id` is returned in the body and the
`X-Request-ID` header and appears on every log line for that request, so a user
report leads straight to the relevant logs. An inbound `X-Request-ID` is reused
so a trace can span services, but it is length-checked and stripped of control
characters first, since it ends up in both a log and a response header.

### `log/slog` rather than zap or logrus

It is in the standard library, it produces structured JSON that aggregators can
query, and its `ReplaceAttr` hook is what makes the credential redaction
possible. One less dependency.

---

## Deliberately not built

Being explicit about scope is more useful than pretending it is complete.

| Not built | Why, and what it would take |
| --- | --- |
| Refresh tokens | A 15-minute access token is enough to demonstrate the JWT requirement. Doing refresh properly means a second token type, persistent storage, rotation and reuse detection — a feature, not a flourish. |
| Token revocation / logout | Needs server-side state (a denylist keyed by `jti`, which is already in every token, or a per-user token version). The hook is there; the storage is not. |
| Rate limiting on login | Belongs at the edge (gateway, reverse proxy) in most deployments, and doing it in-process correctly needs shared state across replicas. |
| Password reset, email verification | Requires an email transport, which is outside the brief. |
| Field-level `PATCH` for `role` | Changing roles is an administrative action that deserves its own audited endpoint rather than riding along with profile edits. |
| A hosted Swagger UI | The OpenAPI document is in the repository; serving a UI would mean either embedding a bundle or depending on a CDN at run time. |
| TLS termination | Normally terminated by the ingress or load balancer. `JWT_SECRET` and bearer tokens assume transport security in front. |

---

## Versions

Go 1.26, `mongo-driver/v2` v2.10, gin v1.12, `golang-jwt/jwt/v5` v5.3,
MongoDB 8. The driver choice is worth one note: v2 removed the `primitive`
package (`bson.ObjectID` now), `mongo.Connect` no longer takes a context, and
`mtest` went internal — so examples written for v1, which is most of what is
online, do not compile against it.
