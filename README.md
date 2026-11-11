# User Management API

A RESTful (and gRPC) user-management service in Go, backed by MongoDB, with
JWT authentication, role-based access control, structured logging, a background
reporting job and a hexagonal architecture.

This is the implementation of **Part 1 — User Management API** of the
[backend-challenge](https://github.com/pitchter/backend-challenge) coding test.
**Part 2 — Lottery Search System** is a design exercise with no code, and its
proposal is in [`docs/lottery-search-design.md`](docs/lottery-search-design.md).

---

## Contents

- [What is implemented](#what-is-implemented)
- [Architecture](#architecture)
- [Quick start](#quick-start)
- [Configuration](#configuration)
- [API reference](#api-reference)
- [gRPC](#grpc)
- [Testing](#testing)
- [Part 2 — Lottery Search System](#part-2--lottery-search-system)
- [Project layout](#project-layout)
- [Further reading](#further-reading)

---

## What is implemented

Every required item, and every bonus item, with a pointer to where it lives.

| # | Requirement | Where |
| --- | --- | --- |
| 1 | User model: `ID`, `Name`, `Email` (unique), hashed `Password`, `CreatedAt` | [`internal/domain/user.go`](internal/domain/user.go) |
| 2 | Registration, authentication returning a JWT, HS256 signing, middleware validation | [`internal/adapter/jwtauth/token.go`](internal/adapter/jwtauth/token.go), [`middleware_auth.go`](internal/adapter/httpapi/middleware_auth.go) |
| 3 | Create, fetch by ID, list, update name/email, delete | [`internal/app/user_service.go`](internal/app/user_service.go), [`handler.go`](internal/adapter/httpapi/handler.go) |
| 4 | MongoDB via the official driver (v2) | [`internal/adapter/mongodb/`](internal/adapter/mongodb) |
| 5 | Logging middleware with method, path and execution time | [`middleware_core.go`](internal/adapter/httpapi/middleware_core.go) |
| 6 | Goroutine logging the total user count every 10 seconds | [`internal/worker/user_counter.go`](internal/worker/user_counter.go) |
| 7 | Unit tests with the standard `testing` package, MongoDB mocked at the port | [`internal/testsupport/`](internal/testsupport), `*_test.go` |

| Bonus | Where |
| --- | --- |
| Containerisation (Docker + docker compose) | [`deploy/`](deploy) |
| Abstraction: Go interfaces over MongoDB | [`internal/domain/ports.go`](internal/domain/ports.go) |
| Input validation (required fields, email format, …) | [`internal/domain/user.go`](internal/domain/user.go), [`httpapi/dto.go`](internal/adapter/httpapi/dto.go) |
| Graceful shutdown with `context.Context` and signals | [`cmd/api/main.go`](cmd/api/main.go) |
| gRPC: `.proto` plus a server secured with token metadata | [`proto/user/v1/user.proto`](proto/user/v1/user.proto), [`internal/adapter/grpcapi/`](internal/adapter/grpcapi) |
| Hexagonal architecture (ports and adapters) | the whole `internal/` tree, enforced by `depguard` |

Beyond the brief, because they matter in production: RBAC with an `admin`/`user`
role, keyset pagination, a single error envelope with correlation ids,
`/healthz` and `/readyz` probes, log redaction of credentials, request-body size
limits, server timeouts, a bootstrap admin, an OpenAPI document and a CI
pipeline.

---

## Architecture

```
          ┌─────────── driving adapters ─────────────┐
          │  Gin HTTP handlers      gRPC server      │
          └──────────────┬───────────────────────────┘
                         │  plain DTOs — no gin or grpc types cross this line
          ┌──────────────▼───────────────────────────┐
          │  application: UserService (use cases)    │
          └──────────────┬───────────────────────────┘
                         │  ports (interfaces declared in the domain)
          ┌──────────────▼───────────────────────────┐
          │  domain: User, Role, errors, ports       │  ← stdlib only
          └──────────────┬───────────────────────────┘
          ┌──────────────▼─── driven adapters ───────┐
          │ mongodb.UserRepository   crypto.Bcrypt   │
          │ jwtauth.TokenService     (fakes in tests)│
          └──────────────────────────────────────────┘
```

Every arrow points inward. `internal/domain` imports nothing but the standard
library, and neither the domain nor the application layer may import gin,
the MongoDB driver, `golang-jwt` or gRPC. That is not a convention anyone has
to remember — `depguard` in [`.golangci.yml`](.golangci.yml) fails the build if
it is violated.

The practical payoff: the gRPC server calls the same `UserService` as REST, so
both transports enforce identical rules with no duplicated logic, and the
use-case tests run against an in-memory repository with no database at all.

---

## Quick start

### With Docker (recommended)

```bash
cp .env.example .env
make secret                  # copy the output into JWT_SECRET in .env
make docker-up
```

That builds the image, starts MongoDB, waits for it to pass its health check,
and then starts the API.

```bash
curl -s localhost:8080/readyz
# {"status":"ok","mongo":"up","version":"docker"}
```

Useful afterwards:

```bash
make docker-logs             # follow the API logs (user_count_report every 10s)
```

```bash
make docker-down             # stop (keeps the data volume)
```

```bash
make docker-clean            # stop and delete the data volume too
```

### Locally, without Docker for the app

Needs Go 1.26+ and a reachable MongoDB.

```bash
cp .env.example .env         # then set JWT_SECRET
docker run -d -p 27017:27017 --name mongo mongo:8
make run
```

### Creating the first admin

Registration always produces a `user`, and only an `admin` can create another
admin — so an empty database needs one admin to be seeded. Set both variables
in `.env` and start the app:

```bash
BOOTSTRAP_ADMIN_EMAIL=admin@example.com
BOOTSTRAP_ADMIN_PASSWORD=change-this-password
```

The seeding is idempotent: if that address already exists, nothing is touched
(the password is not reset either), so restarts are safe.

---

## Configuration

Everything comes from environment variables; a local `.env` is read when
present, and real environment variables always win over the file. The process
validates every value on start-up and **refuses to boot** on a bad one, which
is deliberate — it is better than failing under traffic later.

| Variable | Default | Notes |
| --- | --- | --- |
| `APP_ENV` | `development` | `development` or `production` |
| `HTTP_PORT` | `8080` | |
| `GRPC_PORT` | `50051` | must differ from `HTTP_PORT` |
| `GRPC_ENABLED` | `true` | set `false` to run REST only |
| `MONGO_URI` | `mongodb://localhost:27017` | `mongodb://mongo:27017` under compose |
| `MONGO_DATABASE` | `user_management` | |
| `MONGO_TIMEOUT` | `10s` | connect, ping and per-operation deadline |
| `JWT_SECRET` | — | **required**, ≥ 32 bytes, else the app will not start |
| `JWT_ISSUER` | `user-management-api` | checked on every verification |
| `JWT_AUDIENCE` | `user-management-client` | checked on every verification |
| `JWT_ACCESS_TTL` | `15m` | access-token lifetime |
| `JWT_LEEWAY` | `30s` | tolerance for clock drift |
| `BCRYPT_COST` | `12` | accepted range 10–15 |
| `USER_COUNT_INTERVAL` | `10s` | the background report's period |
| `USER_COUNT_ENABLED` | `true` | |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `json` | `json` or `text` |
| `SHUTDOWN_TIMEOUT` | `15s` | grace period for in-flight requests |
| `HTTP_MAX_BODY_BYTES` | `1048576` | request-body ceiling |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` | Slowloris defence |
| `BOOTSTRAP_ADMIN_NAME` | `Admin` | |
| `BOOTSTRAP_ADMIN_EMAIL` | — | set together with the password |
| `BOOTSTRAP_ADMIN_PASSWORD` | — | set together with the email |

---

## API reference

Base path `/api/v1`. Full request and response examples, including every error
case, are in [`docs/api-examples.md`](docs/api-examples.md); the machine-readable
contract is [`api/openapi.yaml`](api/openapi.yaml).

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `POST` | `/auth/register` | — | Sign up; the result always has role `user` |
| `POST` | `/auth/login` | — | Exchange credentials for an access token |
| `GET` | `/users/me` | any token | The token holder's own profile |
| `POST` | `/users` | admin | Create a user and choose its role |
| `GET` | `/users` | admin | List users (`?limit=&cursor=`) |
| `GET` | `/users/:id` | self or admin | Fetch one user |
| `PATCH` | `/users/:id` | self or admin | Change `name` and/or `email` |
| `DELETE` | `/users/:id` | admin | Delete a user (not your own account) |
| `GET` | `/healthz` | — | Liveness: the process is up |
| `GET` | `/readyz` | — | Readiness: MongoDB answers |

Errors always use one shape, so a client needs a single handler:

```json
{
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "the submitted data is invalid",
    "details": [{ "field": "email", "issue": "must be a valid email address" }]
  },
  "request_id": "9f1c0f3e5c1a4b7d8e2f6a0b4c3d1e2f"
}
```

`code` is part of the contract (the prose may be reworded); `request_id` also
comes back in the `X-Request-ID` header and appears on every log line for that
request.

| Code | Status |
| --- | --- |
| `VALIDATION_ERROR` | 400 |
| `NO_CHANGES` | 400 |
| `BAD_REQUEST` | 400 |
| `UNAUTHENTICATED` | 401 |
| `FORBIDDEN` | 403 |
| `NOT_FOUND` | 404 |
| `METHOD_NOT_ALLOWED` | 405 |
| `EMAIL_TAKEN` | 409 |
| `PAYLOAD_TOO_LARGE` | 413 |
| `INTERNAL_ERROR` | 500 |

### A 60-second tour

```bash
curl -s -X POST localhost:8080/api/v1/auth/register -H 'Content-Type: application/json' -d '{"name":"Somchai Jaidee","email":"somchai@example.com","password":"secret-password"}'
```

```bash
TOKEN=$(curl -s -X POST localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' -d '{"email":"somchai@example.com","password":"secret-password"}' | jq -r .access_token)
```

```bash
curl -s localhost:8080/api/v1/users/me -H "Authorization: Bearer $TOKEN" | jq
```

---

## gRPC

The proto lives at [`proto/user/v1/user.proto`](proto/user/v1/user.proto) and
exposes `CreateUser` and `GetUser`. Authentication uses the same token as REST,
sent as `authorization: Bearer <token>` metadata.

```bash
grpcurl -plaintext localhost:50051 list
```

```bash
grpcurl -plaintext -H "authorization: Bearer $ADMIN_TOKEN" -d '{"name":"Grpc User","email":"grpc@example.com","password":"secret-password","role":"user"}' localhost:50051 user.v1.UserService/CreateUser
```

Server reflection (what lets `grpcurl` work without the proto file) is enabled
only when `APP_ENV` is not `production`. Regenerate the Go code after editing
the proto:

```bash
make proto
```

---

## Testing

```bash
make test
```

Unit tests for every layer, with `-race`. No MongoDB and no Docker needed: the
repository port is substituted with an in-memory fake, so the whole suite runs
in a couple of seconds.

```bash
make test-integration
```

Integration tests that start a real MongoDB 8 in a container through
testcontainers and exercise the actual adapter: CRUD, the unique-email index
under concurrent writes, keyset pagination. They are behind the `integration`
build tag, so `make test` never touches them. Needs Docker running.

```bash
make cover
```

Per-package coverage plus `coverage.html`.

```bash
make check
```

`go vet`, `golangci-lint` and the unit tests together — what CI runs.

A note on mocking, since the brief asks for it: MongoDB is mocked **at the
port**, not at the driver. `mtest` moved under `internal/` in driver v2 and can
no longer be imported, and hand-written fakes that really work let the tests
assert on outcomes instead of on call sequences. The reasoning is spelled out in
[`internal/testsupport/clock.go`](internal/testsupport/clock.go) and
[`docs/design-decisions.md`](docs/design-decisions.md).

---

## Project layout

```
cmd/api/                     the only composition root; no business logic
internal/
  domain/                    entities, business rules, ports (stdlib only)
  app/                       use cases, the single place permissions are decided
  adapter/
    mongodb/                 UserRepository on the official driver v2
    crypto/                  bcrypt password hashing
    jwtauth/                 HS256 token issuing and verification
    httpapi/                 Gin handlers, middleware, error mapping
    grpcapi/                 gRPC server, interceptors, generated code
  platform/                  config, logger, id generation
  worker/                    the 10-second user-count reporter
  testsupport/               in-memory fakes shared by the tests
proto/user/v1/               the .proto contract
api/                         openapi.yaml, requests.http, Postman collection
docs/                        JWT guide, API examples, design decisions
deploy/                      Dockerfile, docker-compose.yml
```

### Renaming the module

The module path is `github.com/pitchter/7-solution`, matching the repository.
To move it under a different account:

```bash
make rename-module NEW=github.com/your-name/your-repo
```

That rewrites `go.mod`, every import, the `go_package` option in the `.proto`,
the `depguard` rules and the `goimports` prefix in `.golangci.yml`, then runs
`go mod tidy` and regenerates the protobuf code. The regeneration is not
optional: `.pb.go` stores `go_package` inside the serialized descriptor as
length-prefixed bytes, which no text substitution can rewrite, and the
`proto-drift` job in CI fails if it is skipped.

The JWT issuer, the compose project name and the local Docker image tag are
deliberately left alone: they happen to read `user-management-api` but are
names, not module paths.

---

## Part 2 — Lottery Search System

The second part of the challenge asks for a design document only: search 10
million lottery tickets by a 6-character wildcard pattern, and never offer the
same ticket to two users at once. No code.

[**docs/lottery-search-design.md**](docs/lottery-search-design.md) is that
proposal. In short:

- A 6-digit pattern is a partially specified integer, so matching is arithmetic
  rather than search — no text index and no search engine are involved.
- Query cost follows the number of **contiguous runs** the pattern covers,
  `10^(w−t)`, not the number of matches. That makes `******` a single range scan
  and `*****5` the genuine worst case at 100,000 runs — the reverse of the
  intuitive ordering.
- The binding requirement is allocation, not retrieval. `SELECT … FOR UPDATE
  SKIP LOCKED` in PostgreSQL satisfies "never the same ticket twice" in one
  statement that selects and reserves together.
- Reservation expiry is enforced in the allocation query itself, so no
  background job is load-bearing for inventory.

---

## Further reading

- [`docs/jwt-guide.md`](docs/jwt-guide.md) — how to generate a secret, obtain a
  token, what is inside it, how to use it over REST and gRPC, and how to verify
  the security properties yourself
- [`docs/api-examples.md`](docs/api-examples.md) — every endpoint with real
  requests and responses, success and failure
- [`docs/design-decisions.md`](docs/design-decisions.md) — the assumptions and
  trade-offs behind the choices above
- [`docs/lottery-search-design.md`](docs/lottery-search-design.md) — Part 2 of
  the challenge: the Lottery Search System design proposal
- [`api/openapi.yaml`](api/openapi.yaml) — the OpenAPI 3.1 contract
