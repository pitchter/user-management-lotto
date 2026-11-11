# JWT guide

How tokens are generated, what is inside them, how to use them, and how to
verify for yourself that the security properties hold.

---

## 1. Generate a signing secret

Tokens are signed with HMAC-SHA256 (`HS256`), so the server holds one shared
secret. Generate a strong one:

```bash
openssl rand -base64 48
```

or, equivalently:

```bash
make secret
```

Put it in `.env`:

```bash
JWT_SECRET=<the generated value>
```

The application **refuses to start** if the secret is missing or shorter than
32 bytes. RFC 7518 section 3.2 requires an HMAC key at least as long as the
hash function's output, and HS256 (HMAC-SHA-256) outputs 32 bytes; a shorter
key weakens the signature and is easier to brute-force. The check lives in
[`internal/platform/config/config.go`](../internal/platform/config/config.go).

Treat the secret like a password: never commit it (`.env` is git-ignored),
never log it (the logger redacts anything called `jwt_secret`, `token`,
`authorization`, …), and rotate it if it leaks. Rotating invalidates every
token already issued, which is the point.

---

## 2. Get a token

Register, then log in:

```bash
curl -s -X POST localhost:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"name":"Somchai Jaidee","email":"somchai@example.com","password":"secret-password"}'
```

```bash
curl -s -X POST localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"somchai@example.com","password":"secret-password"}'
```

```json
{
  "access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJyb2xlIjoidXNlciIsInN1YiI6IjY1M2YxYTJiM2M0ZDVlNmY3MDgxODI4MyIsImlzcyI6InVzZXItbWFuYWdlbWVudC1hcGkiLCJhdWQiOlsidXNlci1tYW5hZ2VtZW50LWNsaWVudCJdLCJleHAiOjE3OTQ5MjM2MDAsIm5iZiI6MTc5NDkyMjcwMCwiaWF0IjoxNzk0OTIyNzAwLCJqdGkiOiI0ZDdkYjIyMWM5NWE3OWUwYThiODc2N2QzZTJmNWE4NyJ9.PLACEHOLDER_SIGNATURE",
  "token_type": "Bearer",
  "expires_in": 900,
  "expires_at": "2026-11-11T11:45:00Z",
  "user": {
    "id": "653f1a2b3c4d5e6f70818283",
    "name": "Somchai Jaidee",
    "email": "somchai@example.com",
    "role": "user",
    "created_at": "2026-11-11T11:30:00Z",
    "updated_at": "2026-11-11T11:30:00Z"
  }
}
```

Keep it in a shell variable for the examples below:

```bash
TOKEN=$(curl -s -X POST localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' -d '{"email":"somchai@example.com","password":"secret-password"}' | jq -r .access_token)
```

The response carries `Cache-Control: no-store`, because a response holding a
token must not be cached by a browser or a proxy.

---

## 3. What is inside a token

Decode the payload yourself — it is base64, not encryption:

```bash
echo "$TOKEN" | cut -d. -f2 | base64 -d 2>/dev/null | jq
```

```json
{
  "role": "user",
  "sub": "653f1a2b3c4d5e6f70818283",
  "iss": "user-management-api",
  "aud": ["user-management-client"],
  "exp": 1794923600,
  "nbf": 1794922700,
  "iat": 1794922700,
  "jti": "4d7db221c95a79e0a8b8767d3e2f5a87"
}
```

| Claim | Meaning |
| --- | --- |
| `sub` | the user's id; the API reads the caller's identity from here |
| `role` | `admin` or `user`; drives every permission decision |
| `iss` | issuer, must equal `JWT_ISSUER` |
| `aud` | audience, must contain `JWT_AUDIENCE` |
| `iat` / `nbf` | issued-at and not-before |
| `exp` | expiry (`JWT_ACCESS_TTL`, 15 minutes by default) |
| `jti` | a unique id per token, for tracing and future revocation |

The header is always:

```json
{ "alg": "HS256", "typ": "JWT" }
```

**Name and email are deliberately absent.** The payload is readable by anyone
holding the token, and copies of profile fields go stale the moment the user
edits them. Only what is needed to authorise a request is carried.

---

## 4. Use the token

### REST

```bash
curl -s localhost:8080/api/v1/users/me -H "Authorization: Bearer $TOKEN"
```

The scheme is case-insensitive per RFC 6750, so `bearer` works too. Anything
else — a missing header, no `Bearer` keyword, an empty token — gets a 401 with
a `WWW-Authenticate: Bearer` header.

### gRPC

Same token, sent as metadata named `authorization`:

```bash
grpcurl -plaintext \
  -H "authorization: Bearer $TOKEN" \
  -d '{"id":"653f1a2b3c4d5e6f70818283"}' \
  localhost:50051 user.v1.UserService/GetUser
```

Both transports call the very same `TokenService`, so a token minted over REST
works over gRPC and vice versa.

### Postman / `.http` files

[`api/requests.http`](../api/requests.http) (VS Code REST Client, JetBrains
HTTP Client) and [`api/postman_collection.json`](../api/postman_collection.json)
both capture the token from the login response automatically; every later
request reuses it.

---

## 5. What makes it expire, and what to do then

Access tokens live for `JWT_ACCESS_TTL` (15 minutes by default). After that,
every protected endpoint answers:

```json
{
  "error": { "code": "UNAUTHENTICATED", "message": "the access token is invalid or has expired" },
  "request_id": "…"
}
```

Log in again to get a new one. There is no refresh token and no server-side
revocation list; the reasoning, and what it would take to add them, is in
[`design-decisions.md`](design-decisions.md). Verification allows
`JWT_LEEWAY` (30s) of clock drift, so machines whose clocks differ slightly do
not reject each other's tokens.

---

## 6. Verify the security properties yourself

Every claim below is also covered by automated tests in
[`internal/adapter/jwtauth/token_test.go`](../internal/adapter/jwtauth/token_test.go).

### A tampered payload is rejected

Flip the role to `admin` in the payload and the signature no longer matches:

```bash
HEADER=$(echo "$TOKEN" | cut -d. -f1); PAYLOAD=$(echo "$TOKEN" | cut -d. -f2 | base64 -d 2>/dev/null | sed 's/"role":"user"/"role":"admin"/' | base64 | tr -d '=\n' | tr '/+' '_-'); curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/api/v1/users -H "Authorization: Bearer $HEADER.$PAYLOAD.$(echo "$TOKEN" | cut -d. -f3)"
```

Expected: `401`.

### `alg: none` is rejected

The classic attack: declare that the token needs no signature and hope the
server believes the header it was handed.

```bash
ALGNONE="$(printf '{"alg":"none","typ":"JWT"}' | base64 | tr -d '=\n' | tr '/+' '_-').$(echo "$TOKEN" | cut -d. -f2)."; curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/api/v1/users -H "Authorization: Bearer $ALGNONE"
```

Expected: `401`. The parser is pinned with
`jwt.WithValidMethods([]string{"HS256"})`, so the `alg` header is never
consulted to decide how to verify. The same pin blocks the other half of the
algorithm-confusion family, where an attacker switches to an asymmetric
algorithm and presents the public key as the HMAC key.

### Other checks performed on every request

| Check | Why |
| --- | --- |
| algorithm is exactly `HS256` | blocks `alg: none` and algorithm confusion |
| signature is valid | the payload has not been edited |
| `exp` is present and in the future | no immortal tokens |
| `nbf` is not in the future | no pre-dated tokens |
| `iss` matches | a token from another system is not accepted |
| `aud` matches | a token minted for another audience is not accepted |
| `sub` is non-empty | the caller must be identifiable |
| `role` is known | an unknown role is refused, never downgraded silently |

A token whose `role` claim is missing entirely is treated as `user`, the lowest
privilege — fail closed, never fail open.

### Finding out which check failed

The client is always told the same thing — `the access token is invalid or has
expired` — so that probing the endpoint reveals nothing. The specific reason is
written to the server log instead, as a `token_rejected` line carrying the same
`request_id` the client was given:

```json
{"level":"WARN","msg":"token_rejected","request_id":"8587...","error":"authentication required: the signature is invalid","path":"/api/v1/users/me"}
```

```bash
docker compose -f deploy/docker-compose.yml --env-file .env logs api | grep token_rejected
```

gRPC logs the same line with `component: grpc` and the full method name.

---

## 7. Permissions

Authentication answers *who you are*; the role decides *what you may do*.

| Action | `user` | `admin` |
| --- | --- | --- |
| Read or edit own profile | yes | yes |
| Read or edit another user | no (403) | yes |
| List all users | no (403) | yes |
| Create a user with a chosen role | no (403) | yes |
| Delete a user | no (403) | yes, except their own account |

Two details worth knowing:

- Signing up cannot grant you `admin`. The sign-up request type has no `role`
  field at all, so there is nothing to filter and nothing to forget to filter.
- For an ordinary user, a forbidden id and a non-existent id both answer 403,
  not 403 and 404 respectively. Otherwise the difference between the two would
  let anyone probe which ids exist.
