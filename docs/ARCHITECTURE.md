# go-cam-server Architecture

## 1. Mục tiêu hệ thống

`go-cam-server` là control plane và stream gateway cho hệ thống camera:

- nhận trạng thái stream từ `MediaMTX`
- kéo lại stream qua RTSP để đưa vào pipeline Go nội bộ
- fan-out sang HLS, storage, MinIO, relay
- cung cấp HTTP API cho monitor, playback, node registry, health, WebRTC signaling
- giữ khả năng thay `MediaMTX` bằng media backend khác về sau

Kiến trúc hiện tại sử dụng ngôn ngữ Golang, khá dễ bảo trì, được gom lại theo hướng dễ maintain hơn, ít phân mảnh hơn, và tập trung control logic vào một đầu mối rõ ràng:

- `MediaMTX` = media plane
- `go-cam-server` = control plane

---

## 2. Topology tổng thể

```text
                         +----------------------+
                         |     Client Apps      |
                         | Flutter / Web / VLC  |
                         +----------+-----------+
                                    |
                         HTTP / HLS / WebRTC / API
                                    |
                                    v
                    +-----------------------------------+
                    |          go-cam-server            |
                    |-----------------------------------|
                    | API + control plane              |
                    | stream manager + fanout          |
                    | monitor priority                 |
                    | relay manager                    |
                    | session / lease / HA hooks       |
                    +---------+-------------+----------+
                              |             |
                    RTSP pull |             | Redis / MinIO / HTTP
                              v             v
                    +----------------+   +------------------+
                    |    MediaMTX    |   | Shared services  |
                    |----------------|   |------------------|
                    | RTMP ingest    |   | Redis registry   |
                    | RTSP source    |   | MinIO recordings |
                    | HLS / WebRTC   |   | future AI event  |
                    | playback API   |   | store / event bus|
                    +--------+-------+   +------------------+
                             ^
                             |
                       RTMP / RTSP / ONVIF
                             |
                    +----------------------+
                    | Streaming Sources    |
                    | RTSP / RTMP / ONVIF  |
                    +----------------------+
```

---

## 3. Runtime deployment

Theo [docker-compose.yml](/Users/steven/Documents/learn/cam/go-cam-server/docker-compose.yml), stack local hiện có 4 service chính:

- `mediamtx`
  media plane, nhận RTMP ingest, expose RTSP/HLS/WebRTC/playback/API
- `server`
  `go-cam-server`, expose HTTP API control plane ở cổng `8080`
- `redis`
  node registry, stream ownership, lease/idempotency store
- `minio`
  object storage cho recording segments

Tất cả chạy trong Docker network riêng `go-cam-network`.

---

## 4. Luồng live stream

### 4.1 Camera -> MediaMTX -> Go pipeline

```text
Camera / Encoder
   |
   | RTMP push
   v
MediaMTX
   |
   | runOnReady hook
   | POST /internal/on-publish?path={streamKey}
   v
go-cam-server
   |
   | IngestManager.Start()
   | RTSP pull from MediaMTX
   v
StreamManager.Register()
   |
   +--> HLSSubscriber
   +--> StorageSubscriber
   +--> MinIOSubscriber
   +--> RelaySubscriber (khi multi-node)
```

Chi tiết entrypoint nằm ở [main.go](/Users/steven/Documents/learn/cam/go-cam-server/cmd/server/main.go), webhook ở [hooks.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/api/hooks.go), và RTSP ingest ở [ingester.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/rtsp/ingester.go).

### 4.2 Stream pipeline trong Go

`go-cam-server` dùng mô hình publisher/subscriber:

- `StreamManager`
  registry in-process của tất cả stream đang live
- `liveStream`
  mỗi stream có ingest channel riêng và một pump goroutine riêng
- `Subscriber`
  HLS, storage, MinIO, livestream viewer, relay

Code chính:

- [manager.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/stream/manager.go)
- [fanout.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/stream/fanout.go)
- [types.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/stream/types.go)
- thư mục [subscriber](/Users/steven/Documents/learn/cam/go-cam-server/internal/subscriber)

Thiết kế hiện tại ưu tiên:

- đơn giản về ownership
- tách rõ publisher và subscriber
- có thể profile theo từng stream
- dễ gắn trace/log toàn trình

---

## 5. Delivery paths

### 5.1 HLS

`HLSSubscriber` ghi segment `.flv` và `index.m3u8` ra thư mục local:

- output local: `./data/hls/{streamKey}/`
- endpoint phục vụ: `GET /hls/{key}/index.m3u8`

Code:

- [hls.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/subscriber/hls.go)
- [streams.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/api/streams.go)

### 5.2 Local archive

`StorageSubscriber` ghi full FLV archive ra local disk:

- output local: `./data/storage/{streamKey}/`
- endpoint phục vụ: `GET /storage/{key}/{file}`

Code:

- [storage.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/subscriber/storage.go)

### 5.3 Object storage

`MinIOSubscriber` gom packet thành segment trong memory rồi upload lên MinIO:

- object path logic nằm trong `internal/minio`
- dùng cho playback/object-based storage về sau

Code:

- [minio.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/subscriber/minio.go)
- [minio](/Users/steven/Documents/learn/cam/go-cam-server/internal/minio)

### 5.4 WebRTC signaling

`go-cam-server` có lớp signaling riêng cho Pion:

- offer endpoint: `POST /pion/webrtc/{key}/offer`
- close session: `DELETE /pion/webrtc/session/{id}`
- demo page: `GET /pion/webrtc/{key}/demo`

Code:

- [pion.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/api/pion.go)
- [pionbridge](/Users/steven/Documents/learn/cam/go-cam-server/internal/pionbridge)

---

## 6. Multi-node và service discovery

### 6.1 Redis registry

Redis hiện được dùng như ephemeral cluster registry:

- `node:{id}` chứa heartbeat / metadata node
- `stream:{key}` ánh xạ stream sang node đang sở hữu

Code:

- [registry.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/node/registry.go)

Mục tiêu hiện tại:

- discovery node
- tìm stream đang nằm ở node nào
- phục vụ relay và monitor cluster

Lưu ý: đây là registry best-effort, chưa phải distributed coordination correctness-critical như `etcd`.

### 6.2 Relay giữa node

Khi stream không ở local node:

1. API tra Redis để biết `sourceNodeID`
2. `RelayManager` mở HTTP relay tới node nguồn
3. node nhận đăng ký một `RelayPublisher` cục bộ
4. subscriber HLS/storage phía node nhận hoạt động như stream local

Code:

- [relay.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/node/relay.go)
- [relay.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/api/relay.go)

---

## 7. HTTP API surface

Router chính nằm ở [server.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/api/server.go).

Các nhóm API chính:

- Control plane
  - `/control/health`
  - `/control/media-mtx`
  - `/control/slo`
- Stream inspection
  - `/streams`
  - `/streams/{key}`
- Monitor priority
  - `/monitor/priority`
  - `/monitor/weights`
  - `/streams/{key}/pin`
  - `/streams/{key}/pin` (`DELETE`)
- Live playback helper
  - `/live/streams`
  - `/live/{key}/urls`
- Playback
  - `/playback/streams`
  - `/playback/{key}/recordings`
  - `/playback/{key}/timespans`
- WebRTC signaling
  - `/pion/webrtc/...`
- Cluster
  - `/nodes`
- Internal hooks
  - `/internal/on-publish`
  - `/internal/on-unpublish`

---

## 8. Monitor priority model

Monitor grid không chọn stream theo thứ tự cố định. Nó dùng scoring runtime để xếp hạng stream theo mức độ quan trọng.

Code:

- [priority.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/stream/priority.go)
- [monitor.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/api/monitor.go)

Các yếu tố hiện tại:

- `viewer_count`
- `bitrate_health` (placeholder)
- `recency`
- `manual_pin`
- `motion_activity` (placeholder)

`manual_pin` là cờ operator gắn tay để giữ camera quan trọng luôn nổi lên.

`motion_activity` hiện chưa nối với nguồn event thật, nhưng đã được chừa weight và API model để mở rộng về sau.

---

## 9. Tracing, logging, health

### 9.1 Tracing

Project đã có nền trace context nhẹ để nối log toàn trình:

- `traceparent`
- `trace_id`
- `span_id`
- `parent_span_id`

Code:

- [tracectx.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/tracectx/tracectx.go)

Trace hiện đi qua:

- HTTP middleware
- MediaMTX hook
- RTSP ingest
- stream manager
- subscriber path
- relay path

Mục tiêu tiếp theo là cắm OTLP exporter/Collector để đẩy sang Jaeger hoặc backend tương đương.

### 9.2 Logging

Structured logging và telemetry helper nằm ở:

- [logging.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/logging/logging.go)

Runtime stats emitter được khởi tạo ở [main.go](/Users/steven/Documents/learn/cam/go-cam-server/cmd/server/main.go), xuất ra:

- stream count
- subscriber count
- goroutine count
- heap / GC stats

### 9.3 Health

`go-cam-server` monitor dependency health cho:

- Redis
- MediaMTX
- MinIO

Code:

- [control.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/api/control.go)
- [health.go](/Users/steven/Documents/learn/cam/go-cam-server/internal/ha/health.go)

---

## 10. MediaMTX integration strategy

Repo đang đi theo hướng:

- build `MediaMTX` từ source local trong `third_party/mediamtx`
- custom như một subproject/service riêng
- `go-cam-server` chỉ phụ thuộc qua adapter/API, không ôm process MediaMTX như trung tâm kiến trúc

Tài liệu build riêng:

- [MEDIAMTX.md](/Users/steven/Documents/learn/cam/go-cam-server/MEDIAMTX.md)
- [Dockerfile.mediamtx](/Users/steven/Documents/learn/cam/go-cam-server/Dockerfile.mediamtx)

Đây là điểm rất quan trọng để về sau:

- thay `MediaMTX` bằng backend media khác
- chạy nhiều node MediaMTX
- thêm policy/control logic mà không phá media plane

---

## 11. AI event extension point

Hiện trong repo chưa có `AiEventService` thật, nhưng kiến trúc đã có chỗ để gắn AI event vào monitor/control plane mà không phải đập lại stream pipeline.

Hướng mở rộng đề xuất:

```text
AI Box / AI Service
   |
   | webhook / queue / poll
   v
go-cam-server
   |
   +--> internal/api/ai_events.go         (future HTTP ingress)
   +--> internal/ai/events.go             (future domain service)
   +--> Redis / DB / queue                (future store)
   +--> stream priority motion_activity   (runtime scoring)
   +--> alert / monitor / playback links  (future UX)
```

### Vai trò của AI event trong kiến trúc hiện tại

- bổ sung `motion_activity` vào `ScoreStream()`
- làm tín hiệu để monitor grid tự đẩy camera nóng lên trên
- liên kết detection event với stream key / camera key / playback timespan

### Điều nên giữ khi triển khai

- AI event là input vào control plane, không nên buộc chặt vào MediaMTX internals
- event ingestion nên idempotent
- score runtime nên tách khỏi lưu trữ lịch sử event
- nếu event rate cao, nên có hàng đệm hoặc queue trung gian

---

## 12. Cấu trúc module hiện tại

```text
go-cam-server/
├── cmd/server                # entrypoint
├── config                    # config loader
├── internal/api             # HTTP API + control plane
├── internal/stream          # StreamManager, ranking, pub/sub core
├── internal/subscriber      # HLS, storage, MinIO, relay, livestream
├── internal/rtsp            # RTSP pull ingest from MediaMTX
├── internal/mediamtx        # MediaMTX client adapter
├── internal/mediamtxproc    # optional subprocess mode
├── internal/node            # Redis registry + relay
├── internal/minio           # object storage adapter
├── internal/ha              # health, leases, idempotency
├── internal/pionbridge      # Pion signaling/service
├── internal/onvif           # ONVIF integration
├── internal/logging         # structured telemetry/log helpers
├── internal/tracectx        # trace propagation foundation
└── third_party/mediamtx     # vendored/customized MediaMTX
```

---

## 13. Nguyên tắc kiến trúc hiện tại

1. `go-cam-server` là control plane, không khóa cứng vào một media engine.
2. `MediaMTX` là service riêng, có thể custom và build độc lập.
3. Stream pipeline trong Go phải quan sát được bằng log/trace/profile.
4. Multi-node ưu tiên đơn giản và thực dụng trước, correctness-critical coordination tính sau.
5. AI event nên được thêm như một input signal vào control plane, không làm bẩn media pipeline lõi.
