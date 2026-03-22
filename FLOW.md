# Go CAM Server — Flow Diagrams

---

## 1. Single Node — Camera Publish Flow

```
┌─────────────┐     RTMP TCP :1935     ┌──────────────────────────────────────────────────────────────┐
│  IP Camera  │ ──────────────────────►│                        Node A                                │
│  (ffmpeg /  │                        │                                                              │
│  real cam)  │                        │  rtmp/server.go                                             │
└─────────────┘                        │  └─ net.Listen(":1935")                                     │
                                       │       └─ per-connection goroutine                           │
                                       │            └─ rtmp/handler.go (CamHandler)                  │
                                       │                 │                                            │
                                       │         OnPublish("cam1")                                   │
                                       │                 │                                            │
                                       │         stream/manager.go                                   │
                                       │         StreamManager.Register(rtmpPublisher)               │
                                       │                 │                                            │
                                       │         newLiveStream("cam1")  ←──── stream/fanout.go       │
                                       │                 │                                            │
                                       │    AddSubscriber × 2 (default)                             │
                                       │         ├─ StorageSubscriber   ←── subscriber/storage.go   │
                                       │         └─ HLSSubscriber       ←── subscriber/hls.go       │
                                       │                 │                                            │
                                       │   OnVideo / OnAudio (each frame)                           │
                                       │         │                                                   │
                                       │         └─► liveStream.Ingest(tag)                         │
                                       │                   │                                         │
                                       │            ┌──────▼──────┐                                 │
                                       │            │  ingest ch  │  buffered chan (64 tags)        │
                                       │            └──────┬──────┘                                 │
                                       │                   │  pump goroutine (1 per stream)          │
                                       │         ┌─────────┴──────────┐                             │
                                       │         ▼                    ▼                              │
                                       │  ┌─────────────┐   ┌──────────────┐                       │
                                       │  │ StorageSub  │   │   HLSSub     │                       │
                                       │  │ ch (512)    │   │   ch (256)   │                       │
                                       │  └──────┬──────┘   └──────┬───────┘                       │
                                       │         │ goroutine        │ goroutine + ticker (2s)        │
                                       │         ▼                  ▼                               │
                                       │  storage/cam1/          hls/cam1/                          │
                                       │  1710000000.flv         seg_0.flv                          │
                                       │  (full archive)         seg_1.flv                          │
                                       │                         seg_2.flv  ← rolling (max 5)      │
                                       │                         index.m3u8                         │
                                       └──────────────────────────────────────────────────────────────┘
```

---

## 2. Single Node — Viewer Access Flow

```
┌──────────────┐   GET /hls/cam1/index.m3u8   ┌────────────────────────────────────────┐
│ Flutter /    │ ────────────────────────────► │  Node A  :8080                         │
│ Browser /    │                               │                                        │
│ ffplay       │                               │  api/streams.go serveHLSPlaylist()     │
└──────────────┘                               │    ├─ os.Stat(hls/cam1/index.m3u8)     │
                                               │    │     exists? ──► http.ServeFile()  │
       ◄─ m3u8 playlist (rolling 5 segs) ──────┤    └─ not found → see multi-node flow │
                                               │                                        │
┌──────────────┐   GET /hls/cam1/seg_N.flv     │  api/streams.go serveHLSSegment()      │
│   Player     │ ─────────────────────────────►│    └─ http.ServeFile(hls/cam1/seg_N)  │
└──────────────┘                               └────────────────────────────────────────┘
       ◄─ FLV video segment ───────────────────┘

─────────────────────────────────────────────────────────────────────────────────────────

┌──────────────┐   GET /storage/cam1/1710.flv  ┌────────────────────────────────────────┐
│   Playback   │ ────────────────────────────► │  api/streams.go serveStorage()         │
│   viewer     │ ◄─ Full FLV file (VoD) ─────── │  Content-Type: video/x-flv             │
└──────────────┘                               └────────────────────────────────────────┘

─────────────────────────────────────────────────────────────────────────────────────────

┌──────────────┐   GET /monitor/priority        ┌────────────────────────────────────────┐
│   Flutter    │ ────────────────────────────► │  api/monitor.go                        │
│   Monitor    │                               │  stream.RankStreams(all, weights, pins) │
│   Screen     │ ◄─ [{cam1, score:0.72}, ─────  │  → sorted by weighted score            │
└──────────────┘      {cam2, score:0.31}]      │    viewer_count×0.4 + bitrate×0.2 ...  │
                                               └────────────────────────────────────────┘
```

---

## 3. Multi-Node — Relay Flow

```
                 ╔══════════════════════════════════════╗
                 ║           Redis (shared)              ║
                 ║  node:node-a → {addr:"A:8080", ...}  ║
                 ║  node:node-b → {addr:"B:8080", ...}  ║
                 ║  stream:cam1 → "node-a"               ║
                 ╚══════════════════════════════════════╝
                          ▲                  ▲
                 heartbeat (5s)          heartbeat (5s)
                          │                  │
┌──────────────────────────────┐    ┌──────────────────────────────┐
│  Node A  :1935 / :8080       │    │  Node B  :1936 / :8081       │
│                              │    │                              │
│  Camera ──► RTMP ──►         │    │  (no camera connected)       │
│    StreamManager             │    │                              │
│    "cam1" stream live        │    │                              │
│                              │    │  [1] Viewer requests         │
│                              │    │  GET /hls/cam1/index.m3u8   │
│                              │    │       │                      │
│                              │    │  [2] Local: not found        │
│                              │    │  [3] Redis.FindStream("cam1")│
│                              │    │      → "node-a"              │
│                              │    │       │                      │
│  [5] API /relay/cam1         │    │  [4] relay.EnsureRelay(      │
│  serveRelay()                │◄───┤       "cam1", "node-a")      │
│    flv.NewEncoder(w)         │    │       │                      │
│    RelaySubscriber           │    │  node/relay.go               │
│    st.AddSubscriber(sub)     │    │  RelayManager.pull()         │
│                              │    │    HTTP GET /relay/cam1 ────►│
│  pump goroutine              │    │    resp.Body = FLV stream    │
│    Deliver(tag) ────────────►│    │       │                      │
│                              │    │  flv.NewDecoder(resp.Body)   │
│  [6] FLV tags streamed       │    │  loop dec.Decode(&tag) {     │
│      over HTTP chunked ─────►│───►│    liveStream.Ingest(&tag)   │
│                              │    │  }                           │
└──────────────────────────────┘    │       │                      │
                                    │  [7] NewLiveStream("cam1")   │
                                    │  (RelayPublisher)            │
                                    │  + StorageSub + HLSSub       │
                                    │       │                      │
                                    │  hls/cam1/seg_0.flv → ready  │
                                    │       │                      │
                                    │  [8] 307 Redirect → retry    │
                                    │  → serveHLSPlaylist()        │
                                    │  → local file found ✓        │
                                    │  → serve m3u8 to viewer      │
                                    └──────────────────────────────┘
                                             │
                                    ┌────────▼──────────┐
                                    │  Viewer gets HLS   │
                                    │  from Node B       │
                                    │  (local segments)  │
                                    └───────────────────┘
```

---

## 4. Multi-Node — Data Flow Detail

```
PUBLISHER SIDE (Node A)                        SUBSCRIBER SIDE (Node B)
─────────────────────────────────────────────────────────────────────────────

Camera                                         Viewer
  │ RTMP push                                    │ GET /hls/cam1/index.m3u8
  ▼                                              ▼
rtmp/handler.go                              api/streams.go
  │ OnVideo(tag)                                 │ registry.FindStream("cam1") → "node-a"
  ▼                                              │ relay.EnsureRelay("cam1", "node-a")
liveStream.Ingest(tag)                           │
  │                                              ▼ (goroutine)
  ▼ buffered chan[64]                        HTTP GET /relay/cam1 ──────► Node A
pump goroutine                                   │                         │
  ├─ StorageSub.Deliver(tag)                      │                    serveRelay()
  ├─ HLSSub.Deliver(tag)                          │                    flv.NewEncoder(w)
  └─ RelaySub.Deliver(tag) ◄─── AddSubscriber ───┤                    RelaySubscriber
         │                                        │                    st.AddSubscriber()
         ▼ buffered chan[128]                      │◄── FLV chunked ────pump.Deliver()
  RelaySubscriber.run()                           │
  enc.Encode(tag) ──────────────────────────────►│
  (HTTP chunked write)                           │
                                            flv.NewDecoder(body)
                                            dec.Decode(&tag)
                                                  │
                                            liveStream.Ingest(&tag)
                                                  │
                                            pump goroutine (Node B)
                                            ├─ StorageSub.Deliver(tag) → storage/cam1/...flv
                                            ├─ HLSSub.Deliver(tag)    → hls/cam1/seg_N.flv
                                            └─ (future: LivestreamSub → WebSocket viewer)
```

---

## 5. Monitor Priority Flow

```
Flutter Monitor Screen
    │
    │  GET /monitor/priority
    ▼
api/monitor.go
    │
    ├─ manager.All() → [cam1, cam2, cam3, cam-lobby, ...]
    │
    ├─ pins.all() → {"cam-lobby": true}
    │
    ├─ weightsStore.get() → {viewer_count:0.4, bitrate:0.2, recency:0.1, pin:0.1, motion:0.2}
    │
    └─ stream.RankStreams(streams, weights, pins)
            │
            ├─ ScoreStream(cam-lobby)
            │    viewer_count: 3 viewers → 0.4 × (3/100) = 0.012
            │    bitrate_health:         → 0.2 × 0.75    = 0.150
            │    recency: 120s online    → 0.1 × 1.0     = 0.100
            │    manual_pin: YES         → 0.1 × 1.0     = 0.100
            │    motion: (TODO)          → 0.2 × 0.0     = 0.000
            │    total score:                              = 0.362
            │
            ├─ ScoreStream(cam-gate)
            │    viewer_count: 0         → 0.000
            │    bitrate_health:         → 0.150
            │    recency: 5s online      → 0.1 × (5/30)  = 0.017
            │    manual_pin: NO          → 0.000
            │    total score:                              = 0.167
            │
            └─ sort descending by score
                [cam-lobby: 0.362, cam-gate: 0.167, ...]
                │
                ▼
    Response: { streams: [{key:"cam-lobby", score:0.362, pinned:true}, ...] }
                │
                ▼
    Flutter fills N grid slots in priority order:
    ┌──────────────┬──────────────┬──────────────┐
    │  cam-lobby   │  cam-gate    │  cam-3       │
    │  (score 0.36)│  (score 0.17)│  (score 0.10)│
    └──────────────┴──────────────┴──────────────┘
```

---

## 6. Cluster Node Discovery Flow

```
                 ┌─────────────────────────────────────┐
                 │              Redis                   │
                 │                                      │
  Node A ──────► │  SET node:node-a {...} EX 15        │ ◄────── Node B
  heartbeat      │  SET node:node-b {...} EX 15        │         heartbeat
  (every 5s)     │  SET stream:cam1 "node-a" EX 30     │         (every 5s)
                 └─────────────────────────────────────┘

  GET /nodes (any node)
    ├─ KEYS node:*
    ├─ filter: UpdatedAt < 10s ago (same threshold as C++ KeepAliveService)
    └─ return [] NodeInfo

  node/selector.go SelectNode(nodes, preferRole)
    → picks node with minimum StreamCount (least-load)
    → mirrors C++ GetNodeAvaliableService.cpp exactly

  Stream location: GET /streams/cam1 on Node B
    ├─ manager.Get("cam1") → not found locally
    ├─ registry.FindStream("cam1") → "node-a"
    └─ response includes: { node_id: "node-a(relay)", hls_url: "http://B:8081/hls/cam1/..." }
```

---

## 7. Component Map (Code → Flow Step)

```
 File                              Role in flow
─────────────────────────────────────────────────────────────────────────────
 internal/rtmp/server.go           TCP listener, goroutine-per-connection
 internal/rtmp/handler.go          Decode RTMP → FLV tag → liveStream.Ingest()
 internal/stream/fanout.go         liveStream + pump goroutine (core fan-out)
 internal/stream/manager.go        Registry: Register / Unregister / Get / All
 internal/stream/types.go          Interfaces: Publisher, Subscriber, Stream
 internal/stream/priority.go       ScoreStream() / RankStreams() for monitor
 internal/subscriber/storage.go    Write FLV to disk (playback archive)
 internal/subscriber/hls.go        Segment FLV into rolling m3u8 playlist
 internal/subscriber/livestream.go GOP cache + push to viewer connection
 internal/subscriber/relay.go      Encode FLV tags to HTTP (Node A relay out)
 internal/node/relay.go            Pull FLV from HTTP (Node B relay in)
 internal/node/registry.go         Redis heartbeat + stream location
 internal/node/selector.go         Least-load node selection
 internal/redis/client.go          Redis client (graceful no-Redis fallback)
 internal/api/server.go            chi router, wires all handlers
 internal/api/streams.go           /streams /hls /storage + cross-node redirect
 internal/api/relay.go             /relay/{key} → FLV stream to peer node
 internal/api/nodes.go             /nodes → cluster status
 internal/api/monitor.go           /monitor/priority + /monitor/weights
 cmd/server/main.go                Wire all components, graceful shutdown
```

---

## 8. Subscriber Channel Buffer Strategy

```
Subscriber Type    Buffer Size    Rationale
─────────────────────────────────────────────────────────────────────────────
ingest (stream)        64         1–2 keyframe intervals at 30fps
StorageSubscriber     512         Large: absorbs disk I/O spikes
HLSSubscriber         256         Medium: segment boundary buffering
LivestreamSubscriber   64         Small: low-latency, drop > buffer
RelaySubscriber       128         Medium: network RTT buffer

Rule: Deliver() is always non-blocking.
      If channel full → increment dropped counter → continue.
      The pump goroutine is NEVER blocked by a slow subscriber.
      This is the Go equivalent of the C++ Engine task queue guarantee.
```

---

## 9. Future: gRPC Relay (Phase 2)

```
Current (POC):     HTTP FLV streaming   — simple, no codegen, works now
Phase 2:           gRPC bidirectional   — lower latency, backpressure

proto/media.proto:
  service AppService {
    rpc ListLiveStreams(ListStreamsRequest) returns (ListStreamsResponse);
    rpc GetMonitorPriority(MonitorPriorityRequest) returns (MonitorPriorityResponse);
    rpc CreateLiveSession(CreateSessionRequest) returns (CreateSessionResponse);
  }
  service RelayService {
    rpc PullStream(PullStreamRequest) returns (stream FLVPacket);
    rpc SyncNodeState(NodeStateRequest) returns (NodeStateResponse);
  }

Replace in node/relay.go:
  http.Get("/relay/key") → grpcClient.RelayStream(key)
Replace in api/relay.go:
  http.ResponseWriter → grpc.ServerStream

Generate: make proto-gen
```
