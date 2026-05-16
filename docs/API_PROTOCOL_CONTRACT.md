# Go CAM Server API and Protocol Contract

This document is the current backend contract for splitting `go-cam-server`
into multiple services later. It is based on the route table in
`internal/api/server.go` and the concrete handlers under `internal/api`,
`internal/auth`, `internal/relay`, `internal/stream`, `internal/rtsp`,
`internal/node`, and `internal/subscriber`.

## 1. Service Boundary Map

The current process contains these logical services:

| Boundary | Current package | Responsibility | Future service candidate |
|---|---|---|---|
| API Gateway / Control API | `internal/api` | HTTP routing, auth middleware, JSON contracts, binary streaming endpoints | `api-gateway` |
| Auth | `internal/auth` | Login, JWT issue/verify, role permissions, token invalidation | `auth-service` |
| Live Session Orchestrator | `internal/api/live_session_handler.go`, `internal/stream/engine.go`, `internal/relay` | Ensure shared stream, create viewer session, negotiate delivery protocol | `session-service` |
| Stream Runtime | `internal/stream` | In-process pub/sub, fan-out, stream registry, packet lifetime | `stream-runtime` |
| Media Ingest | `internal/rtsp`, `internal/mediamtx` | React to MediaMTX hooks, pull RTSP, normalize packets | `ingest-service` |
| Relay / Edge | `internal/node`, `internal/api/relay.go`, `internal/subscriber/relay.go` | Cross-node stream discovery and FLV relay | `relay-service` |
| Playback / Recording | `internal/subscriber/storage.go`, `internal/subscriber/minio.go`, `internal/minio` | Local FLV archives, MinIO/S3 segments, listing/presigned URLs | `recording-service` |
| Camera Adapter | `internal/api/onvif.go`, `internal/camera`, `internal/onvif` | ONVIF discovery, info, snapshot, PTZ, camera source resolution | `camera-service` |
| Control Plane Health | `internal/api/control.go`, `internal/ha` | Dependency health, SLO config, session leases, idempotency | `control-plane` |

## 2. Common HTTP Rules

Base server address comes from `http.addr` in `server.yml`, default `:8080`.

### Headers

| Header | Direction | Meaning |
|---|---|---|
| `Authorization: Bearer <token>` | Request | Required for protected `/api/v1/*` routes except login |
| `Content-Type: application/json` | Request | Required for JSON bodies |
| `traceparent` | Request/response | W3C trace context. Middleware accepts incoming value or creates one and returns it |
| `X-Request-ID` | Request | Optional request id; chi middleware also creates request ids |
| `Idempotency-Key` | Request | Supported by `POST /live/{key}/session` |
| `Idempotency-Replayed: true` | Response | Returned when an idempotent response is replayed |

### Error Shapes

There are two current error conventions:

```json
{ "code": "bad_request", "message": "human readable" }
```

```json
{ "error": "human readable" }
```

`/api/v1/live-sessions` adds trace correlation:

```json
{ "code": "stream_unavailable", "message": "details", "trace_id": "..." }
```

For a multi-service split, standardize all JSON routes on:

```json
{
  "code": "stable_machine_code",
  "message": "human readable",
  "trace_id": "optional"
}
```

Note: several ONVIF handlers currently use `http.Error` inside the JSON route
group, so the content type can say JSON while the body is plain text. Treat that
as a compatibility issue to fix before making those endpoints public.

### Debug Surface

The server mounts chi's pprof profiler at `/debug`. This should stay private to
operators and must not be exposed through a public gateway.

## 3. Auth Contract

Base path: `/api/v1/auth`

### `POST /api/v1/auth/login`

Auth: none.

Request:

```json
{
  "username": "admin",
  "password": "password123"
}
```

Success `200`:

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

Errors:

| Status | Code |
|---|---|
| `400` | `bad_request` |
| `401` | `invalid_credentials` |
| `403` | `account_disabled` |
| `500` | `internal_error` |

### `GET /api/v1/auth/session`

Auth: bearer token.

Success `200`:

```json
{
  "user_id": "uuid",
  "username": "admin",
  "role": "admin",
  "permissions": ["camera:view", "camera:manage", "relay:manage", "user:manage"],
  "expires_at": "2026-05-16T15:00:00Z"
}
```

### `DELETE /api/v1/auth/session`

Auth: bearer token.

Success: `204 No Content`. The current token is invalidated in the in-memory
auth store.

### Roles

| Role | Permissions |
|---|---|
| `admin` | `camera:view`, `camera:manage`, `relay:manage`, `user:manage` |
| `operator` | `camera:view` |

Auth JWT claims are HS256 signed and include `jti`, `sub`, and `role`. Logout
requires server-side token storage, so moving auth to another service requires a
shared token/session store or changing revocation semantics.

## 4. Preferred Live Session Contract

This is the newer authenticated contract and should be the starting point for a
future multi-service API.

Base path: `/api/v1/live-sessions`

### `POST /api/v1/live-sessions`

Auth: bearer token with `camera:view`.

Request:

```json
{
  "cam_id": "cam-001",
  "profile_id": "hd",
  "client_capabilities": {
    "delivery_protocols": ["webrtc", "hls"]
  }
}
```

Rules:

| Field | Required | Rule |
|---|---:|---|
| `cam_id` | yes | Camera identifier |
| `profile_id` | yes | Stream profile identifier |
| `client_capabilities.delivery_protocols` | no | Ordered preference. Supported: `webrtc`, `hls`. Default: `["webrtc","hls"]` |

Success `201`:

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

Errors:

| Status | Code | Meaning |
|---|---|---|
| `400` | `bad_request` | Invalid JSON or missing `cam_id` / `profile_id` |
| `401` | `unauthorized` | Missing/invalid auth token |
| `403` | `forbidden` | Token lacks `camera:view` |
| `503` | `stream_unavailable` | Ingest disabled, startup failed, or stream did not register within 5s |
| `503` | `relay_unavailable` | Relay session could not be created |

How it works:

1. Build canonical `StreamKey` as `{cam_id}/{profile_id}`.
2. `StreamEngine.EnsureStream` returns existing live stream or starts RTSP ingest.
3. The engine waits up to 5 seconds for `StreamManager.Register`.
4. A `viewer_session_id` UUID is created.
5. `relay.Manager.Create` negotiates protocol, creates endpoint
   `{relay.base_url}/relay/live/{protocol}/{viewer_session_id}`, and signs a
   relay JWT.
6. Response returns playback endpoint and relay token.

### `DELETE /api/v1/live-sessions/{viewer_session_id}`

Auth: bearer token with `camera:view`.

Success: `204 No Content`. This closes the relay session only. The shared stream
continues while ingest is active or other viewers exist.

## 5. Legacy Live Lease Contract

These routes are unauthenticated today. They provide reconnect leases and are
useful for client continuity, but should be folded into the authenticated
session-service contract before exposing externally.

### `POST /live/{key}/session`

Auth: none.

Path:

| Param | Rule |
|---|---|
| `key` | Stream key without `/` because the chi route is `/live/{key}/session` |

Request body is optional:

```json
{
  "tenant_id": "tenant-1",
  "device_id": "cam-001",
  "viewer_id": "viewer-123",
  "preferred_protocol": "hls",
  "last_known_endpoint": "http://..."
}
```

Defaults:

| Field | Default |
|---|---|
| `tenant_id` | `default` |
| `device_id` | stream key |
| `viewer_id` | `anonymous` |
| `preferred_protocol` | `hls` unless `hls` or `webrtc` |

Success `201`:

```json
{
  "session_id": "uuid",
  "stream_key": "cam1",
  "tenant_id": "default",
  "device_id": "cam1",
  "viewer_id": "anonymous",
  "route_status": "live",
  "mode": "normal",
  "control_plane_backend": "redis",
  "issued_at": "2026-05-16T10:00:00Z",
  "lease_expires_at": "2026-05-16T10:01:30Z",
  "reattach_token": "<hmac-token>",
  "urls": {
    "hls": "http://localhost:8080/hls/cam1/index.m3u8",
    "webrtc": "http://localhost:8080/pion/webrtc/cam1/offer",
    "webrtc_offer_url": "http://localhost:8080/pion/webrtc/cam1/offer"
  },
  "retry_policy": {
    "initial_delay_ms": 200,
    "max_delay_ms": 5000,
    "multiplier": 2,
    "max_attempts": 6,
    "jitter_fraction": 0.2
  }
}
```

`Idempotency-Key` replays the exact successful response for the configured TTL.

### `POST /live/session/reattach`

Auth: none.

Request:

```json
{ "reattach_token": "<token from create session>" }
```

Success `200`: same body shape as create session, with renewed lease expiry and
token. If the stream is not currently routable, `route_status` becomes
`last_known` and `warning` is set.

Errors:

| Status | Meaning |
|---|---|
| `400` | Invalid body or missing token |
| `401` | Invalid token signature/claims |
| `404` | Lease not found |
| `410` | Lease expired |
| `500` | Reattach failed |

## 6. Stream and Delivery API

### `GET /streams`

Returns all local `StreamManager` streams plus ready MediaMTX paths that are not
already represented locally.

Success `200`:

```json
[
  {
    "key": "cam1",
    "node_id": "node-1",
    "is_live": true,
    "started_at": "2026-05-16T10:00:00Z",
    "subscriber_count": 3,
    "packets_total": 12345,
    "hls_url": "http://localhost:8080/hls/cam1/index.m3u8",
    "webrtc_url": "http://localhost:8080/pion/webrtc/cam1/offer",
    "webrtc_offer_url": "http://localhost:8080/pion/webrtc/cam1/offer"
  }
]
```

### `GET /streams/{key}`

Returns one stream. Local streams include subscriber details.

Success `200`:

```json
{
  "key": "cam1",
  "node_id": "node-1",
  "is_live": true,
  "started_at": "2026-05-16T10:00:00Z",
  "subscriber_count": 2,
  "packets_total": 1000,
  "hls_url": "http://localhost:8080/hls/cam1/index.m3u8",
  "webrtc_url": "http://localhost:8080/pion/webrtc/cam1/offer",
  "webrtc_offer_url": "http://localhost:8080/pion/webrtc/cam1/offer",
  "subscribers": [
    { "id": "hls-cam1", "type": "hls", "dropped_packets": 0 }
  ]
}
```

Errors: `404 {"error":"stream not found"}`.

### `POST /streams/{key}/pin`

Pins a stream for monitor priority.

Success `200`:

```json
{ "status": "pinned", "key": "cam1" }
```

### `DELETE /streams/{key}/pin`

Success `200`:

```json
{ "status": "unpinned", "key": "cam1" }
```

### `GET /live/streams`

Returns MediaMTX paths when MediaMTX is enabled. Falls back to local streams
otherwise.

Success `200`:

```json
{
  "streams": [
    {
      "stream_key": "cam1",
      "ready": true,
      "source": "publisher",
      "bytes_received": 123456,
      "hls_url": "http://localhost:8888/cam1/index.m3u8",
      "webrtc_url": "http://localhost:8080/pion/webrtc/cam1/offer",
      "webrtc_offer_url": "http://localhost:8080/pion/webrtc/cam1/offer",
      "rtsp_url": "rtsp://localhost:8554/cam1",
      "rtmp_url": "rtmp://localhost:1935/cam1"
    }
  ],
  "total": 1
}
```

### `GET /live/{key}/urls`

Returns direct URLs for one stream.

Success `200`:

```json
{
  "stream_key": "cam1",
  "hls": "http://localhost:8888/cam1/index.m3u8",
  "webrtc": "http://localhost:8080/pion/webrtc/cam1/offer",
  "webrtc_offer_url": "http://localhost:8080/pion/webrtc/cam1/offer",
  "rtsp": "rtsp://localhost:8554/cam1",
  "rtmp": "rtmp://localhost:1935/cam1",
  "pion_demo_url": "/pion/webrtc/cam1/demo",
  "pion_offer_method": "POST"
}
```

### `GET /live/{key}/ws`

WebSocket binary stream. The server attaches a `LivestreamSubscriber`, sends the
GOP cache, then writes binary packet payloads with `websocket.BinaryMessage`.

Current wire body is raw `AVPacket.Data` bytes, not a stable framed API. Do not
use this as a cross-service protocol without adding a frame header containing
packet type, timestamp, keyframe flag, and payload length.

### `GET /hls/{key}/index.m3u8`

Binary/static HLS playlist.

Responses:

| Status | Body |
|---|---|
| `200` | `application/x-mpegURL`, local `index.m3u8` |
| `307` | Redirect to same URL after starting cross-node relay; `Retry-After: 2` |
| `503` | Stream is local but HLS has not produced first segment |
| `404` | Stream not found locally or in Redis registry |
| `502` | Relay start failed |

The HLS implementation currently writes `.flv` segment files into an m3u8
playlist. It works for VLC/ffplay style clients but is not browser-native HLS.

### `GET /hls/{key}/{file}`

Serves one HLS segment file from `<hls.root_path>/<key>/<file>`.

### `GET /storage/{key}/{file}`

Serves local playback FLV from `<storage.root_path>/<key>/<file>`.

Success content type: `video/x-flv`.

## 7. WebRTC Signaling Contract

### `POST /pion/webrtc/{key}/offer`

Auth: none.

Request accepts either flat offer fields:

```json
{ "type": "offer", "sdp": "v=0..." }
```

or nested:

```json
{
  "offer": { "type": "offer", "sdp": "v=0..." }
}
```

Success `200`:

```json
{
  "stream_key": "cam1",
  "session_id": "uuid",
  "type": "answer",
  "sdp": "v=0...",
  "ttl_seconds": 120,
  "close_url": "/pion/webrtc/session/<session_id>",
  "playback_url": "/playback/cam1/timespans"
}
```

How it works:

1. Build source RTSP URL: `{pion_webrtc.source_rtsp_base_url}/{key}`.
2. Use `gortsplib` to describe/setup/play the RTSP stream.
3. Select supported video/audio tracks.
4. Create a Pion peer connection.
5. Add RTP tracks and forward RTSP RTP packets into WebRTC tracks.
6. Return SDP answer.

Errors: `400` invalid request, `503` Pion disabled, `502` RTSP/WebRTC bridge
failure.

### `DELETE /pion/webrtc/session/{id}`

Success `200`:

```json
{ "status": "closed", "session_id": "uuid" }
```

### `GET /pion/webrtc/{key}/demo`

Returns a demo HTML page that calls the offer endpoint.

## 8. Playback Contract

### `GET /playback/streams`

If MinIO is enabled, returns stream keys with `recordings/{streamKey}/...`
objects. If MinIO is disabled and MediaMTX is enabled, returns MediaMTX paths.

Success:

```json
{
  "streams": ["cam1", "cam2"],
  "total": 2,
  "source": "mediamtx"
}
```

`source` is only present in the MediaMTX fallback path.

### `GET /playback/{key}/recordings?limit=N`

Requires MinIO.

Success `200`:

```json
{
  "stream_key": "cam1",
  "total": 1,
  "available_total": 12,
  "recordings": [
    {
      "object_key": "recordings/cam1/1778912345678.flv",
      "stream_key": "cam1",
      "size_bytes": 1234567,
      "last_modified": "2026-05-16T10:00:00Z",
      "presigned_url": "http://minio/..."
    }
  ]
}
```

Errors: `503` when MinIO is unavailable.

### `GET /playback/{key}/timespans?limit=N`

Requires MediaMTX playback API.

Success `200`:

```json
{
  "stream_key": "cam1",
  "total": 1,
  "available_total": 10,
  "timespans": [
    {
      "start": "2026-05-16T10:00:00Z",
      "duration": 60,
      "url": "http://localhost:9996/get?path=cam1&start=...&duration=60&format=mp4"
    }
  ]
}
```

## 9. Control Plane Contract

### `GET /control/health`

Success `200`:

```json
{
  "status": "healthy",
  "mode": "normal",
  "checked_at": "2026-05-16T10:00:00Z",
  "dependencies": [
    { "name": "redis", "required": true, "status": "up", "latency_ms": 2 }
  ],
  "topology": {
    "node_id": "node-1",
    "region": "dev-us",
    "az": "dev-us-1a"
  }
}
```

If any required dependency is down, `status` becomes `degraded` and `mode`
becomes `degraded_control_plane`.

### `GET /control/media-mtx`

Requires MediaMTX.

Success:

```json
{
  "status": "up",
  "checked_at": "2026-05-16T10:00:00Z",
  "path_count": 3,
  "ready_path_count": 2,
  "bytes_received": 123456,
  "paths": [
    { "name": "cam1", "ready": true, "source": "publisher", "bytes_received": 123456 }
  ],
  "api_base_url": "http://localhost:9997",
  "playback_base_url": "http://localhost:9996"
}
```

### `GET /control/slo`

Success:

```json
{
  "slo": {
    "api_availability_target": 99.95,
    "api_failure_budget_percent": 0.05,
    "stream_start_success_target": 99.9,
    "stream_failure_budget": 0.1,
    "first_frame_p95_ms_target": 2500,
    "reconnect_success_target": 99.9,
    "reconnect_failure_budget": 0.1
  }
}
```

## 10. Cluster and Relay Contract

### `GET /nodes`

If Redis registry is unavailable:

```json
{
  "nodes": [],
  "note": "Redis not configured - single-node mode"
}
```

With Redis:

```json
{
  "nodes": [
    {
      "id": "node-1",
      "http_addr": "localhost:8080",
      "role": "master",
      "stream_count": 2,
      "updated_at": "2026-05-16T10:00:00Z"
    }
  ]
}
```

Redis schema:

| Key | Type | Value | TTL |
|---|---|---|---|
| `node:{nodeID}` | string JSON | `NodeInfo` | 15s |
| `stream:{streamKey}` | string | owning `nodeID` | 30s |

### `GET /relay/{key}`

Internal node-to-node media endpoint.

Response:

| Header | Value |
|---|---|
| `Content-Type` | `video/x-flv` |
| `Cache-Control` | `no-cache` |
| `X-Stream-Key` | stream key |
| `X-Node-ID` | source node id |

Body: standard FLV file header, followed by FLV tags over an open HTTP
connection. The connection stays open until the source stream ends or the
consumer disconnects.

How cross-node HLS relay works:

1. Viewer asks Node B for `/hls/{key}/index.m3u8`.
2. Node B does not have local HLS file.
3. Node B reads `stream:{key}` in Redis and finds Node A.
4. Node B calls `RelayManager.EnsureRelay`.
5. Node B starts HTTP `GET http://{nodeA.http_addr}/relay/{key}`.
6. Node A writes FLV stream from its local `StreamManager`.
7. Node B registers a synthetic `RelayPublisher`.
8. Node B attaches local Storage and HLS subscribers.
9. Viewer retries `/hls/{key}/index.m3u8` and gets Node B local HLS.

### `POST /internal/relay/sessions`

Internal relay session API used by the v1 live-session handler.

Request:

```json
{
  "relay_session_id": "uuid",
  "stream_id": "uuid",
  "cam_id": "cam-001",
  "requested_protocols": ["webrtc", "hls"],
  "token_expires_at": "2026-05-16T23:00:00Z"
}
```

Success `201`:

```json
{
  "relay_session_id": "uuid",
  "protocol": "webrtc",
  "endpoint": "http://localhost:8080/relay/live/webrtc/<relay_session_id>",
  "token": "<relay JWT>"
}
```

Relay JWT claims:

```json
{
  "jti": "relay_session_id",
  "relay_session_id": "uuid",
  "stream_id": "uuid",
  "cam_id": "cam-001",
  "protocol": "webrtc",
  "iat": 1778912345,
  "exp": 1778941145
}
```

### `DELETE /internal/relay/sessions/{relay_session_id}`

Success: `204 No Content`.

## 11. MediaMTX Hook Contract

These are called by `mediamtx.yml`.

### `POST /internal/on-publish?path={key}&source_type={type}&source_id={id}&traceparent={trace}`

Returns `200` immediately after starting RTSP ingest. If MediaMTX ingest is
disabled, returns `200` and does nothing.

How it works:

1. Validate `path`.
2. Create/propagate trace context.
3. `IngestManager.Start(ctx, key)` is called.
4. A goroutine pulls `rtsp://.../{key}` from MediaMTX.
5. The stream is registered in `StreamManager`.
6. Storage, HLS, and optional MinIO subscribers are attached.

### `POST /internal/on-unpublish?path={key}`

Stops RTSP ingest for the key and unregisters the stream.

## 12. Camera and ONVIF Contract

These routes are currently public and unauthenticated.

### `GET /onvif/discover?nic=eth0`

Success:

```json
{
  "count": 1,
  "devices": ["http://192.168.1.50/onvif/device_service"]
}
```

`nic` is optional. Empty means scan all interfaces.

### `GET /onvif/info?endpoint=...&username=...&password=...`

Returns ONVIF device information from the camera.

### `GET /onvif/snapshot?endpoint=...&username=...&password=...`

Returns `image/jpeg` body fetched from the camera snapshot URI.

### `POST /onvif/resolve`

Request:

```json
{
  "endpoint": "http://192.168.1.50:80/onvif/device_service",
  "username": "admin",
  "password": "pass"
}
```

Returns normalized camera stream/profile information from the ONVIF adapter.

### `POST /camera/resolve`

Canonical adapter endpoint for new code.

Manual request:

```json
{
  "source": "manual",
  "camera_id": "frontdoor",
  "manual_main": "rtsp://192.168.1.10/stream1",
  "manual_sub": "rtsp://192.168.1.10/stream2"
}
```

ONVIF request:

```json
{
  "source": "onvif",
  "endpoint": "http://192.168.1.50:80/onvif/device_service",
  "username": "admin",
  "password": "pass"
}
```

### `POST /onvif/ptz`

Request:

```json
{
  "endpoint": "http://192.168.1.50/onvif/device_service",
  "username": "admin",
  "password": "pass",
  "pan": 0.1,
  "tilt": 0,
  "zoom": 0,
  "stop": false
}
```

Success:

```json
{ "success": true }
```

### `POST /onvif/reboot`

Request:

```json
{
  "endpoint": "http://192.168.1.50/onvif/device_service",
  "username": "admin",
  "password": "pass"
}
```

Success:

```json
{ "status": "rebooting" }
```

## 13. Monitor Priority Contract

### `GET /monitor/priority`

Success:

```json
{
  "weights": {
    "viewer_count": 0.4,
    "bitrate_health": 0.2,
    "recency": 0.1,
    "manual_pin": 0.1,
    "motion_activity": 0.2
  },
  "streams": [
    {
      "stream_key": "cam1",
      "score": 0.362,
      "viewers": 3,
      "age_seconds": 120,
      "pinned": true,
      "reasons": ["viewer=0.012(n=3)", "bitrate=0.150", "recency=0.100(120s)", "pin=0.100", "motion=0.000(todo)"],
      "scored_at": "2026-05-16T10:00:00Z"
    }
  ],
  "total": 1
}
```

### `PUT /monitor/weights`

Request:

```json
{
  "viewer_count": 0.5,
  "bitrate_health": 0.2,
  "recency": 0.1,
  "manual_pin": 0.1,
  "motion_activity": 0.1
}
```

Success:

```json
{
  "weights": {
    "viewer_count": 0.5,
    "bitrate_health": 0.2,
    "recency": 0.1,
    "manual_pin": 0.1,
    "motion_activity": 0.1
  }
}
```

## 14. Internal Media Protocols

### `AVPacket`

This is the in-process packet shape used by `StreamManager`:

| Field | Type | Meaning |
|---|---|---|
| `Type` | `uint8` | FLV tag type: `8` audio, `9` video, `18` metadata |
| `Timestamp` | `uint32` | milliseconds relative to stream start |
| `IsKeyframe` | bool | keyframe marker for GOP handling |
| `Data` | bytes | raw FLV tag body |

Packets are pooled and reference-counted. `Deliver` is non-blocking; if a
subscriber buffer is full, the packet is dropped for that subscriber.

### HTTP FLV Relay

Use this for current node-to-node relay only:

```text
HTTP/1.1 200 OK
Content-Type: video/x-flv
Transfer-Encoding: chunked

FLV header + FLV tags...
```

### Proposed gRPC Relay (`proto/media.proto`)

The repo already has a future-oriented gRPC sketch:

```proto
service RelayService {
  rpc PullStream(PullStreamRequest) returns (stream FLVPacket);
  rpc SyncNodeState(NodeStateRequest) returns (NodeStateResponse);
}

message FLVPacket {
  uint32 type = 1;
  uint32 timestamp_ms = 2;
  bytes payload = 3;
}
```

This is the right direction for service-to-service relay because it makes packet
framing explicit. Before implementing, add:

| Field | Why |
|---|---|
| `bool is_keyframe` | preserve GOP/keyframe behavior |
| `string traceparent` or metadata | propagate tracing |
| `string stream_id` | distinguish stream instance from stream key |
| auth metadata | protect internal relay |

## 15. External Protocols

| Boundary | Protocol | Current implementation |
|---|---|---|
| Camera to MediaMTX | RTMP / RTSP publish | MediaMTX listens on `:1935` / `:8554` |
| MediaMTX hook to Go | HTTP POST | `/internal/on-publish`, `/internal/on-unpublish` |
| MediaMTX to Go ingest | RTSP pull | `gortsplib`, URL `{media_mtx.rtsp_base_url}/{key}` |
| Go local media fan-out | in-memory `AVPacket` | `StreamManager` pump + subscribers |
| Go to viewer HLS | HTTP m3u8 + FLV segments | `/hls/{key}/index.m3u8`, `/hls/{key}/seg_N.flv` |
| Go to viewer WebRTC | HTTP SDP offer/answer + WebRTC RTP | Pion bridge |
| Go to node | HTTP FLV stream | `/relay/{key}` |
| Go to Redis | RESP | node heartbeat, stream ownership, leases |
| Go to MinIO/S3 | S3 API | recording upload/list/presign |
| Go to MediaMTX control | HTTP JSON | `/v3/paths/list`, `/v3/config/global/get`, playback `/list` and `/get` |

## 16. End-to-End Flow

### Camera publish through MediaMTX

```text
Camera -> MediaMTX publish path cam1
MediaMTX runOnReady -> POST /internal/on-publish?path=cam1
Go IngestManager -> RTSP pull rtsp://localhost:8554/cam1
RTSP packets -> FLV-body AVPacket
StreamManager.Register(cam1)
Attach StorageSubscriber + HLSSubscriber + optional MinIOSubscriber
Stream pump -> non-blocking fan-out to subscribers
```

### Viewer using preferred v1 live session

```text
Client -> POST /api/v1/auth/login
Client -> POST /api/v1/live-sessions { cam_id, profile_id, protocols }
Session handler -> StreamEngine.EnsureStream(cam_id/profile_id)
Engine -> IngestManager.Start if needed
Relay manager -> create viewer relay session and token
Client -> use returned endpoint + relay token
Client -> DELETE /api/v1/live-sessions/{viewer_session_id} when done
```

### Cross-node HLS

```text
Node A owns cam1 and writes stream:cam1 -> node-a in Redis
Viewer requests Node B /hls/cam1/index.m3u8
Node B misses local file, finds node-a in Redis
Node B starts GET http://node-a/relay/cam1
Node A streams HTTP FLV
Node B registers RelayPublisher(cam1)
Node B HLS subscriber writes local segments
Viewer retries and gets HLS from Node B
```

## 17. Multi-Service Split Recommendations

1. Make `/api/v1/live-sessions` the public stable session API.
2. Move legacy `/live/{key}/session` reattach semantics into `/api/v1/live-sessions/{id}/reattach` or similar, with auth.
3. Replace path-param stream keys with query/body fields or catch-all routes before using `{cam_id}/{profile_id}` keys in URL paths. Current chi routes like `/live/{key}/urls` cannot carry a slash inside `{key}`.
4. Standardize all errors before service extraction.
5. Persist auth users/tokens and live-session state outside process memory.
6. Promote `proto/media.proto` to the service-to-service relay contract, but add `is_keyframe`, trace metadata, auth metadata, and stream instance id.
7. Protect all `/internal/*`, `/relay/*`, and ONVIF control routes before exposing nodes outside a private network.
8. Decide whether HLS means browser-native HLS. Current segments are FLV files referenced by m3u8.
9. Keep MediaMTX as a separate media-plane dependency; the Go services should own control/session/recording contracts, not raw codec conversion unless needed.
10. Treat Redis keys as internal implementation today. If cluster state becomes a service API, expose it through a registry service instead of sharing Redis schema with every service.
