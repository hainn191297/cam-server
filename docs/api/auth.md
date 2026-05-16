# Auth API

Base path: `/api/v1/auth`

All responses are `application/json`. Error bodies follow the shape:

```json
{ "code": "error_code", "message": "human readable" }
```

---

## POST /api/v1/auth/login

Authenticates a user and returns a signed JWT.

**Auth:** None required.

### Request

```json
{
  "username": "admin",
  "password": "s3cr3t"
}
```

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `username` | string | yes | |
| `password` | string | yes | |

### Responses

| Status | Code | Description |
|--------|------|-------------|
| 200 | — | Login successful |
| 400 | `bad_request` | Missing or malformed body |
| 401 | `invalid_credentials` | Wrong username or password |
| 403 | `account_disabled` | Account exists but is inactive |
| 500 | `internal_error` | Token issue failed |

**200 body:**

```json
{
  "token": "<HS256 JWT>",
  "expires_at": "2026-05-16T15:00:00Z",
  "user": {
    "user_id": "uuid",
    "username": "admin",
    "role": "admin"
  }
}
```

**Example:**

```bash
curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"password123"}'
```

---

## GET /api/v1/auth/session

Returns information about the current session.

**Auth:** `Authorization: Bearer <token>`

### Responses

| Status | Code | Description |
|--------|------|-------------|
| 200 | — | Session info |
| 401 | `unauthorized` | Missing, expired, or revoked token |

**200 body:**

```json
{
  "user_id": "uuid",
  "username": "admin",
  "role": "admin",
  "permissions": ["camera:view", "camera:manage", "relay:manage", "user:manage"],
  "expires_at": "2026-05-16T15:00:00Z"
}
```

**Example:**

```bash
curl -s http://localhost:8080/api/v1/auth/session \
  -H 'Authorization: Bearer <token>'
```

---

## DELETE /api/v1/auth/session

Logs out the current session. The token is immediately invalidated server-side.

**Auth:** `Authorization: Bearer <token>`

### Responses

| Status | Description |
|--------|-------------|
| 204 | Logged out (also returned if no session was active) |

**Example:**

```bash
curl -s -X DELETE http://localhost:8080/api/v1/auth/session \
  -H 'Authorization: Bearer <token>'
```

---

## Roles & Permissions

| Role | Permissions |
|------|-------------|
| `admin` | `camera:view`, `camera:manage`, `relay:manage`, `user:manage` |
| `operator` | `camera:view` |

Permissions are checked per-route via `RequirePermission(perm)` middleware. A `403` is returned if the token's role lacks the required permission.

---

## Token details

- Algorithm: HS256
- Secret: configured via `auth.jwt_secret`
- TTL: configurable (`auth.token_ttl_hours`, default 8 h)
- Stored in-memory (Phase 1); logout is immediate via server-side invalidation list
- `jti` claim is the server-assigned `token_id`; revocation is checked on every request
