# Live Sessions API

Live sessions represent a viewer's connection to a camera stream. Creating one
starts the camera ingest if it isn't already running, assigns a relay gateway
session, and returns a playback endpoint + short-lived relay token.

Base path: `/api/v1/live-sessions`

All responses are `application/json`. Error bodies:

```json
{ "code": "error_code", "message": "human readable", "trace_id": "uuid" }
```

---

## POST /api/v1/live-sessions

Provision a live-stream session for a viewer.

**Auth:** `Authorization: Bearer <token>` — requires `camera:view` permission.

### Request

```json
{
  "cam_id": "cam-001",
  "profile_id": "hd",
  "client_capabilities": {
    "delivery_protocols": ["webrtc", "hls"]
  }
}
```

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `cam_id` | string | yes | Camera identifier |
| `profile_id` | string | yes | Stream profile (e.g. `hd`, `sd`) |
| `client_capabilities.delivery_protocols` | string[] | no | Preferred protocols in priority order. Supported: `webrtc`, `hls`. Defaults to `["webrtc","hls"]` |

### Protocol negotiation

The server picks the first protocol from `delivery_protocols` that it supports.
WebRTC is preferred; HLS is the safe fallback. If the list is empty or omitted,
`webrtc` is tried first.

### Responses

| Status | Code | Description |
|--------|------|-------------|
| 201 | — | Session created |
| 400 | `bad_request` | Missing `cam_id` or `profile_id`, or malformed JSON |
| 401 | `unauthorized` | Missing or invalid token |
| 403 | `forbidden` | Token lacks `camera:view` |
| 503 | `stream_unavailable` | Camera ingest failed to start within timeout, or ingest is disabled |
| 503 | `relay_unavailable` | Relay gateway could not be provisioned |

**201 body:**

```json
{
  "viewer_session_id": "uuid",
  "stream_id": "uuid",
  "protocol": "webrtc",
  "endpoint": "http://relay.example/relay/live/webrtc/<viewer_session_id>",
  "token": "<relay JWT>",
  "expires_at": "2026-05-16T23:00:00Z",
  "capabilities": {
    "supports_audio": false,
    "reconnect": true
  }
}
```

| Field | Notes |
|-------|-------|
| `viewer_session_id` | UUID identifying this viewer's relay session |
| `stream_id` | UUID of the shared stream (stable across multiple viewer sessions) |
| `protocol` | Negotiated protocol: `webrtc` or `hls` |
| `endpoint` | Playback URL on the relay gateway |
| `token` | Short-lived relay JWT — present this to the relay gateway |
| `expires_at` | Token expiry (default 8 h from creation) |

**Example:**

```bash
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -d '{"username":"admin","password":"password123"}' | jq -r .token)

curl -s -X POST http://localhost:8080/api/v1/live-sessions \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "cam_id": "cam-001",
    "profile_id": "hd",
    "client_capabilities": { "delivery_protocols": ["webrtc","hls"] }
  }'
```

---

## DELETE /api/v1/live-sessions/{viewer_session_id}

Close a viewer's relay session. The relay gateway will stop forwarding to this
viewer. The underlying camera stream continues if other viewers are connected.

**Auth:** `Authorization: Bearer <token>` — requires `camera:view` permission.

### Path parameters

| Parameter | Description |
|-----------|-------------|
| `viewer_session_id` | UUID returned by POST /api/v1/live-sessions |

### Responses

| Status | Description |
|--------|-------------|
| 204 | Session closed (also returned if the session was not found) |

**Example:**

```bash
curl -s -X DELETE \
  http://localhost:8080/api/v1/live-sessions/f2e711b9-fb66-4b83-9eba-c91a4e15ac63 \
  -H "Authorization: Bearer $TOKEN"
```

---

## Internal relay endpoints

These are called between server nodes, not by external clients.

### POST /internal/relay/sessions

Called by the live-session handler on the local node to create a relay session
on the relay gateway node.

**Auth:** Server-to-server (no user token — called internally only).

Request / response mirrors the relay `CreateSessionRequest` / `CreateSessionResponse` types.

### DELETE /internal/relay/sessions/{relay_session_id}

Closes a relay session by its relay-session ID (same as `viewer_session_id`).

Returns `204`.

---

## Stream lifecycle

```
POST /api/v1/live-sessions
  │
  ├─ StreamEngine.EnsureStream(cam_id/profile_id)
  │    ├─ If already live → return existing stream_id
  │    └─ If new → IngestManager.Start() → wait up to 5 s for StreamManager.Register()
  │
  └─ RelayManager.Create(viewer_session_id, stream_id, protocol)
       └─ Returns endpoint + relay JWT
```

A `503 stream_unavailable` is returned if ingest does not register the stream
within 5 seconds, or if ingest is not configured (`mediamtx.enabled: false`).
