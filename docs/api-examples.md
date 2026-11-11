# API examples

Every endpoint with a real request and a real response, success and failure.
All of these were captured against the service running under
`make docker-up`; only ids, timestamps and request ids were tidied for
readability.

Base URL: `http://localhost:8080`, base path `/api/v1`.

Ready-to-run versions of all of this:

- [`api/requests.http`](../api/requests.http) — VS Code REST Client / JetBrains HTTP Client
- [`api/postman_collection.json`](../api/postman_collection.json) — Postman
- [`api/openapi.yaml`](../api/openapi.yaml) — the OpenAPI 3.1 contract

---

## Conventions

Every response carries `X-Request-ID`, which also appears in `request_id` on
errors and on every log line for that request — quote it when reporting a
problem.

Errors always use one shape:

```json
{
  "error": {
    "code": "MACHINE_READABLE_CODE",
    "message": "human readable prose",
    "details": [{ "field": "email", "issue": "what is wrong with it" }]
  },
  "request_id": "9f1c0f3e5c1a4b7d8e2f6a0b4c3d1e2f"
}
```

`details` is present only when specific fields are at fault. Match on `code`,
never on `message`.

---

## POST /api/v1/auth/register

Public sign-up. The new user always gets role `user`.

```bash
curl -i -X POST localhost:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"name":"Somchai Jaidee","email":"Somchai@Example.com","password":"secret-password"}'
```

**201 Created**

```
HTTP/1.1 201 Created
Location: /api/v1/users/653f1a2b3c4d5e6f70818283
X-Request-Id: a1b2c3d4e5f60718293a4b5c6d7e8f90
Content-Type: application/json; charset=utf-8
```

```json
{
  "id": "653f1a2b3c4d5e6f70818283",
  "name": "Somchai Jaidee",
  "email": "somchai@example.com",
  "role": "user",
  "created_at": "2026-11-11T11:40:19.316643387Z",
  "updated_at": "2026-11-11T11:40:19.316643387Z"
}
```

Note that the submitted `Somchai@Example.com` came back lower-cased: addresses
are normalised on the way in, so `A@b.com` and `a@b.com` are the same account.
The response has no password field of any kind.

### 409 — the email is already registered

```bash
curl -s -X POST localhost:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"name":"Somchai Jaidee","email":"somchai@example.com","password":"secret-password"}'
```

```json
{
  "error": { "code": "EMAIL_TAKEN", "message": "that email is already registered" },
  "request_id": "a76ea1be8e6303fcd8460640f1cef94b"
}
```

This verdict comes from a unique index in MongoDB, not from a check in the
application, so two simultaneous sign-ups with the same address cannot both
win.

### 400 — invalid fields (all of them reported at once)

```bash
curl -s -X POST localhost:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"name":"A","email":"not-an-email","password":"short"}'
```

```json
{
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "the submitted data is invalid",
    "details": [
      { "field": "name", "issue": "must be at least 2 characters long" },
      { "field": "email", "issue": "must be a valid email address" },
      { "field": "password", "issue": "must be at least 8 characters long" }
    ]
  },
  "request_id": "4747d03f606c8a682f357ad28e0088e8"
}
```

### 400 — unreadable body

```bash
curl -s -X POST localhost:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' -d '{"name":'
```

```json
{
  "error": {
    "code": "BAD_REQUEST",
    "message": "the request could not be read; it must be valid JSON in the documented shape"
  },
  "request_id": "ccd25a9c3fc643727e6f53505cc09a4a"
}
```

### 413 — body over the limit

A body larger than `HTTP_MAX_BODY_BYTES` (1 MiB by default) is refused before
it is parsed:

```json
{
  "error": { "code": "PAYLOAD_TOO_LARGE", "message": "the request body exceeds 1048576 bytes" },
  "request_id": "…"
}
```

---

## POST /api/v1/auth/login

```bash
curl -s -X POST localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"somchai@example.com","password":"secret-password"}'
```

**200 OK** (with `Cache-Control: no-store`)

```json
{
  "access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ...",
  "token_type": "Bearer",
  "expires_in": 900,
  "expires_at": "2026-11-11T11:55:19.783709262Z",
  "user": {
    "id": "653f1a2b3c4d5e6f70818283",
    "name": "Somchai Jaidee",
    "email": "somchai@example.com",
    "role": "user",
    "created_at": "2026-11-11T11:40:19.115Z",
    "updated_at": "2026-11-11T11:40:19.115Z"
  }
}
```

Capture it for the calls below:

```bash
TOKEN=$(curl -s -X POST localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' -d '{"email":"somchai@example.com","password":"secret-password"}' | jq -r .access_token)
```

### 401 — wrong password

```json
{
  "error": { "code": "UNAUTHENTICATED", "message": "invalid email or password" },
  "request_id": "686bea12a3035ad7cb8dc3a1468c394f"
}
```

### 401 — unknown email

```json
{
  "error": { "code": "UNAUTHENTICATED", "message": "invalid email or password" },
  "request_id": "b7096658e48d882647afcbb5ed192cbc"
}
```

**The two are byte-for-byte identical on purpose**, and both take about the
same time (a bcrypt comparison runs even when no user was found). Otherwise
either the wording or the response time would reveal which addresses are
registered.

---

## GET /api/v1/users/me

The token holder's own profile — no need to decode `sub` client-side.

```bash
curl -s localhost:8080/api/v1/users/me -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{
  "id": "653f1a2b3c4d5e6f70818283",
  "name": "Somchai Jaidee",
  "email": "somchai@example.com",
  "role": "user",
  "created_at": "2026-11-11T11:40:19.115Z",
  "updated_at": "2026-11-11T11:40:19.115Z"
}
```

### 401 — no token

```bash
curl -s localhost:8080/api/v1/users
```

```json
{
  "error": {
    "code": "UNAUTHENTICATED",
    "message": "an Authorization: Bearer <token> header is required"
  },
  "request_id": "4696dd9f51d56a516338764ea56456b2"
}
```

The response also carries `WWW-Authenticate: Bearer realm="user-management-api"`.

---

## GET /api/v1/users (admin)

```bash
curl -s 'localhost:8080/api/v1/users?limit=2' -H "Authorization: Bearer $ADMIN_TOKEN"
```

**200 OK**

```json
{
  "data": [
    {
      "id": "653f1a2b3c4d5e6f70818280",
      "name": "Admin",
      "email": "admin@example.com",
      "role": "admin",
      "created_at": "2026-11-11T11:39:50.149Z",
      "updated_at": "2026-11-11T11:39:50.149Z"
    },
    {
      "id": "653f1a2b3c4d5e6f70818283",
      "name": "Somchai Jaidee",
      "email": "somchai@example.com",
      "role": "user",
      "created_at": "2026-11-11T11:40:19.115Z",
      "updated_at": "2026-11-11T11:40:19.115Z"
    }
  ],
  "meta": {
    "count": 2,
    "limit": 2,
    "next_cursor": "653f1a2b3c4d5e6f70818283",
    "total": 3
  }
}
```

### Paging

Pass `meta.next_cursor` back as `?cursor=` to get the following page; a
`next_cursor` of `null` means there is nothing more.

```bash
curl -s 'localhost:8080/api/v1/users?limit=2&cursor=653f1a2b3c4d5e6f70818283' -H "Authorization: Bearer $ADMIN_TOKEN"
```

This is keyset pagination, not offset pagination: pages cost the same whether
they are the first or the thousandth, and a sign-up during paging cannot make a
record appear twice or vanish.

`limit` defaults to 20 and is capped at 100.

### 403 — not an admin

```bash
curl -s localhost:8080/api/v1/users -H "Authorization: Bearer $TOKEN"
```

```json
{
  "error": { "code": "FORBIDDEN", "message": "this endpoint is restricted to administrators" },
  "request_id": "8dc8c29e43463fd555dee99a92d4b00f"
}
```

### 400 — nonsensical query

```bash
curl -s 'localhost:8080/api/v1/users?limit=0' -H "Authorization: Bearer $ADMIN_TOKEN"
```

```json
{
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "the submitted data is invalid",
    "details": [{ "field": "limit", "issue": "must be at least 1" }]
  },
  "request_id": "66b8047f4fb384765cc7a3d2d7e309f6"
}
```

Omitting `limit` entirely uses the default; sending `limit=0` is a mistake and
is reported as one. A `cursor` that is not a 24-character hex id is rejected
the same way.

---

## POST /api/v1/users (admin)

The only way to create an `admin`.

```bash
curl -s -X POST localhost:8080/api/v1/users \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Second Admin","email":"admin2@example.com","password":"another-secret","role":"admin"}'
```

**201 Created**

```json
{
  "id": "653f1a2b3c4d5e6f70818287",
  "name": "Second Admin",
  "email": "admin2@example.com",
  "role": "admin",
  "created_at": "2026-11-11T11:40:20.960108804Z",
  "updated_at": "2026-11-11T11:40:20.960108804Z"
}
```

`role` may be omitted, in which case it is `user`.

### 400 — unknown role

```json
{
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "the submitted data is invalid",
    "details": [{ "field": "role", "issue": "must be one of: admin, user" }]
  },
  "request_id": "f374be894a987564aa19b4ef83699c31"
}
```

---

## GET /api/v1/users/:id

The user itself, or any admin.

```bash
curl -s localhost:8080/api/v1/users/653f1a2b3c4d5e6f70818283 -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{
  "id": "653f1a2b3c4d5e6f70818283",
  "name": "Somchai Jaidee",
  "email": "somchai@example.com",
  "role": "user",
  "created_at": "2026-11-11T11:40:19.115Z",
  "updated_at": "2026-11-11T11:40:19.115Z"
}
```

### 404 — no such user

```json
{
  "error": { "code": "NOT_FOUND", "message": "the requested user does not exist" },
  "request_id": "213c93db33510e529dbb8a4be23c6ed2"
}
```

A malformed id (not a 24-character hex string) also answers 404, not 500: it
cannot match anything, so "not found" is the honest answer.

### 403 — someone else's record

An ordinary user asking for another id gets 403 **whether or not that id
exists**, so the status code cannot be used to enumerate accounts.

---

## PATCH /api/v1/users/:id

Change `name`, `email`, or both. The user itself, or any admin.

```bash
curl -s -X PATCH localhost:8080/api/v1/users/653f1a2b3c4d5e6f70818283 \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Somchai J. Updated"}'
```

**200 OK**

```json
{
  "id": "653f1a2b3c4d5e6f70818283",
  "name": "Somchai J. Updated",
  "email": "somchai@example.com",
  "role": "user",
  "created_at": "2026-11-11T11:40:19.115Z",
  "updated_at": "2026-11-11T11:40:42.535802383Z"
}
```

Only what was sent changes: `email` kept its value, `created_at` is untouched
and `updated_at` moved. Omitting a field leaves it alone; sending it empty is
an attempt to clear it and fails validation.

### 400 — nothing to change

```bash
curl -s -X PATCH localhost:8080/api/v1/users/653f1a2b3c4d5e6f70818283 \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{}'
```

```json
{
  "error": { "code": "NO_CHANGES", "message": "send at least one field to change (name or email)" },
  "request_id": "18751f51a49d9210b1e0459a28102cf8"
}
```

### 409 — the new email belongs to someone else

```json
{
  "error": { "code": "EMAIL_TAKEN", "message": "that email is already registered" },
  "request_id": "82383f8990ada47848ad976317da4bb3"
}
```

Resubmitting your own current address is fine — you do not collide with
yourself.

### 403 — editing another user

```json
{
  "error": { "code": "FORBIDDEN", "message": "you are not allowed to perform this action" },
  "request_id": "38fb2a4f21351cca75f2843ff219545e"
}
```

---

## DELETE /api/v1/users/:id (admin)

```bash
curl -i -X DELETE localhost:8080/api/v1/users/653f1a2b3c4d5e6f70818283 \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

**204 No Content** — no body, which is what a successful delete means.

Fetching it afterwards:

```json
{
  "error": { "code": "NOT_FOUND", "message": "the requested user does not exist" },
  "request_id": "3cc4896842ed5e8ad474f2efbee79cdc"
}
```

Deleting it again answers 404 as well.

### 403 — deleting your own admin account

```json
{
  "error": { "code": "FORBIDDEN", "message": "you are not allowed to perform this action" },
  "request_id": "776e76fa64f224dd200c09f6be8d5b24"
}
```

Were the last admin able to delete itself, nobody could administer the system
again without editing the configuration and restarting.

---

## Health probes

```bash
curl -s localhost:8080/healthz
```

```json
{ "status": "ok", "version": "docker" }
```

```bash
curl -s localhost:8080/readyz
```

```json
{ "status": "ok", "mongo": "up", "version": "docker" }
```

`/healthz` never touches MongoDB: a brief database outage should not make an
orchestrator kill otherwise healthy pods. `/readyz` does, and answers **503**
with `"mongo":"down"` when the database is unreachable, so the instance is
pulled out of the load balancer until it recovers.

---

## Routing errors

### 404 — unknown endpoint

```bash
curl -s localhost:8080/api/v1/nope
```

```json
{
  "error": { "code": "NOT_FOUND", "message": "no such endpoint" },
  "request_id": "139a493251d6fc0944080963f79c6b23"
}
```

### 405 — wrong method

```bash
curl -s -X PUT localhost:8080/api/v1/auth/login
```

```json
{
  "error": { "code": "METHOD_NOT_ALLOWED", "message": "that method is not allowed on this endpoint" },
  "request_id": "24eac30587d75a95e9c9c1ea33f23e10"
}
```

Both are JSON in the usual shape, not a framework's HTML error page.

---

## gRPC

Same tokens, same rules. See [`jwt-guide.md`](jwt-guide.md) for the metadata
format.

### CreateUser

```bash
grpcurl -plaintext \
  -H "authorization: Bearer $ADMIN_TOKEN" \
  -d '{"name":"Grpc User","email":"grpc@example.com","password":"secret-password","role":"user"}' \
  localhost:50051 user.v1.UserService/CreateUser
```

```json
{
  "user": {
    "id": "653f1a2b3c4d5e6f70818289",
    "name": "Grpc User",
    "email": "grpc@example.com",
    "role": "user",
    "createdAt": "2026-11-11T11:40:43.051834342Z",
    "updatedAt": "2026-11-11T11:40:43.051834342Z"
  }
}
```

### GetUser

```bash
grpcurl -plaintext \
  -H "authorization: Bearer $TOKEN" \
  -d '{"id":"653f1a2b3c4d5e6f70818283"}' \
  localhost:50051 user.v1.UserService/GetUser
```

### Errors

No metadata:

```
ERROR:
  Code: Unauthenticated
  Message: metadata "authorization: Bearer <token>" is required
```

A `user` token calling `CreateUser`:

```
ERROR:
  Code: PermissionDenied
  Message: you are not allowed to perform this action
```

| Domain error | HTTP | gRPC |
| --- | --- | --- |
| validation failed | 400 | `InvalidArgument` |
| no fields to update | 400 | `InvalidArgument` |
| authentication required | 401 | `Unauthenticated` |
| invalid credentials | 401 | `Unauthenticated` |
| not permitted | 403 | `PermissionDenied` |
| user not found | 404 | `NotFound` |
| email already registered | 409 | `AlreadyExists` |
| anything unexpected | 500 | `Internal` |

The same error means the same thing over both protocols, because both map from
one set of domain errors.

---

## What the logs look like

Every request produces exactly one line, with the method, the route, the
status and how long it took — the requirement from the brief, plus the fields
that make an incident diagnosable:

```json
{"time":"2026-11-11T11:40:19.12Z","level":"INFO","msg":"http_request","method":"POST","path":"/api/v1/auth/register","status":201,"duration_ms":64.312,"bytes":201,"client_ip":"192.168.65.1","request_id":"a1b2c3d4e5f60718293a4b5c6d7e8f90"}
```

```json
{"time":"2026-11-11T11:40:29.65Z","level":"INFO","msg":"user_count_report","component":"user_counter","total":3,"duration_ms":1.815}
```

The route is the registered pattern (`/api/v1/users/:id`), not the concrete
path, so lines aggregate into useful statistics and no user id is logged
unnecessarily. Authenticated requests also carry `user_id` and `role`.
Credentials never appear: anything named `password`, `authorization`, `token`,
`jwt_secret` and friends is replaced with `[REDACTED]` by the logger itself.
