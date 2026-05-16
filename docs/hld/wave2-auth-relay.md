# HLD: Wave 2 — Auth, Relay, and Live Sessions

## Context

Wave 2 adds viewer-facing access control and a relay session layer on top of the existing Wave 1 pub/sub streaming core. It enables:

- Authenticated HTTP API with role-based access control
- Per-viewer relay gateway sessions (WebRTC or HLS) backed by a short-lived signed token
- High-level stream lifecycle coordination via `StreamEngine`

---

## New Components

```
┌─────────────────────────────────────────────────────────┐
│                   HTTP API (chi router)                 │
│                                                         │
│  POST /api/v1/auth/login         ── AuthHandler         │
│  GET  /api/v1/auth/session       ── AuthHandler         │
│  DELETE /api/v1/auth/session     ── AuthHandler         │
│                                                         │
│  POST /api/v1/live-sessions      ── LiveSessionHandler  │
│  DELETE /api/v1/live-sessions/:id── LiveSessionHandler  │
│                                                         │
│  POST /internal/relay/sessions   ── relay.Handler       │
│  DELETE /internal/relay/sessions/:id ── relay.Handler   │
└────────┬────────────────────┬────────────────────────────┘
         │                    │
         ▼                    ▼
  ┌─────────────┐      ┌──────────────┐
  │ AuthService │      │ StreamEngine │
  │  (auth/)    │      │ (stream/)    │
  │             │      │              │
  │ JWT issue   │      │ EnsureStream │
  │ JWT verify  │      │  (idempotent)│
  │ bcrypt login│      └──────┬───────┘
  │ token store │             │
  └─────────────┘             ▼
                        ┌──────────────┐      ┌──────────────┐
                        │ StreamManager│      │ IngestManager│
                        │ (stream/)    │◄─────│  (rtsp/)     │
                        │              │      │              │
                        │ Register     │      │ Start / Stop │
                        │ Unregister   │      │  RTSP pull   │
                        │ OnRegister ──┼─────►│  (optional)  │
                        │ OnUnregister │      └──────────────┘
                        └──────┬───────┘
                               │
                               ▼
                        ┌──────────────┐
                        │ relay.Manager│
                        │  (relay/)    │
                        │              │
                        │ Create sess  │
                        │ Close sess   │
                        │ Reaper loop  │
                        └──────────────┘
```

---

## Auth (`internal/auth/`)

### Design decisions

| Decision | Rationale |
|----------|-----------|
| In-memory store (Phase 1) | Avoids DB dependency for initial delivery; token invalidation list lives in the same process |
| HS256 JWT | Stateless bearer token; validated on every request via store lookup + signature check |
| Separate JWT secret from relay secret | Auth tokens must not be accepted by the relay gateway, and vice versa |
| Timing-safe login | `bcrypt.CompareHashAndPassword` always runs even for unknown usernames to prevent timing-based enumeration |

### Token lifecycle

```
Login → bcrypt verify → Issue JWT (jti = token_id) → SaveToken → Return signed string
Request → ExtractBearer → jwt.Parse → GetToken(jti) → check InvalidatedAt → check ExpiresAt → OK
Logout → InvalidateToken(jti)
```

### Role model

```
admin    → camera:view, camera:manage, relay:manage, user:manage
operator → camera:view
```

Permissions are embedded in the role; no dynamic ACL store is needed at this stage.

---

## Stream Engine (`internal/stream/engine.go`)

`Engine` sits above `StreamManager` and `IngestManager`. It provides an idempotent `EnsureStream(key)` call that:

1. Returns an existing live stream immediately (fast path, read lock only)
2. On miss, starts the ingest under a write lock (slow path)
3. Polls `StreamManager.Get` for up to 5 s waiting for the publisher to register
4. Returns an error if ingest is nil (MediaMTX disabled) or if the stream does not appear within timeout

State machine per `StreamKey`:

```
requested → starting → live
                    ↘ stopped (on ingest error or timeout)
```

`EnsureStream` is safe for concurrent callers: double-checked locking under write lock prevents duplicate ingest starts for the same key.

---

## Relay (`internal/relay/`)

Each `relay.Session` represents one viewer's gateway connection. It is distinct from the shared `stream.Stream` (which serves all subscribers).

### Session lifecycle

```
POST /api/v1/live-sessions
  └─ Manager.Create(RelaySessionID, StreamID, protocols)
       ├─ Protocol negotiation (webrtc preferred, hls fallback)
       ├─ Endpoint: {baseURL}/relay/live/{protocol}/{relaySessionID}
       └─ TokenService.Issue(session) → relay JWT (separate secret)

DELETE /api/v1/live-sessions/{id}
  └─ Manager.Close(relaySessionID)

Background (every 30 s): Manager.ReapIdle(idleTimeout)
```

### Relay JWT claims

```json
{
  "relay_session_id": "uuid",
  "stream_id": "uuid",
  "cam_id": "cam-001",
  "protocol": "webrtc",
  "exp": <unix timestamp>
}
```

The relay gateway (not yet implemented) validates this token before forwarding media.

---

## StreamManager → Redis sync

When `registry` (Redis-backed node registry) is available, `StreamManager`
fires `OnRegister` / `OnUnregister` callbacks after each state change. These
callbacks call `registry.AnnounceStream` / `registry.RevokeStream` so peer
nodes can locate the owning node for inter-node relay.

Callbacks run in a goroutine (non-blocking relative to the publisher's write
path). Failures are logged as warnings but do not fail the stream registration.

---

## Security notes

- Auth tokens and relay tokens use separate HS256 secrets (`auth.jwt_secret`, `relay.secret`)
- bcrypt default cost (10) on login — adds ~280 ms per request by design
- Middleware runs before every `/api/v1/*` route; unprotected paths (`/internal/*`, `/hls/*`, `/relay/*`) are server-to-server or separate trust boundaries
- Token revocation is immediate (in-memory store); no TTL-based grace period

---

## Open items (deferred)

| Item | Deferred to |
|------|-------------|
| Persist auth store to SQLite | Wave 3 |
| Real bitrate in priority scoring (`stream/priority.go:45`) | Wave 3 (stats integration) |
| AI motion event integration (`stream/priority.go:67`) | Wave 4 |
| Relay gateway implementation (forward WebRTC/HLS to viewer) | Wave 3 |
| Multi-node session lookup (viewer reconnect to any node) | Wave 3 |
