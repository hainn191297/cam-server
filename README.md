# Go Cam Server 🎥🚀

![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?style=for-the-badge&logo=go)
![Architecture](https://img.shields.io/badge/Architecture-Pub%2FSub-ff69b4?style=for-the-badge)
![Status](https://img.shields.io/badge/Status-Active-success?style=for-the-badge)

**Go Cam Server** is an ultra-high-performance, low-latency camera streaming server written entirely in Golang. It is specifically designed to tackle the core challenges of the video streaming industry: **Handling tens of thousands of concurrent streams with ultra-low latency.**

---

## Key Features

### 1. Pub/Sub Fan-out Architecture
The stream model operates as a robust multi-pipeline engine: A single inbound Source (Publisher) seamlessly fans out to infinite outbound Destinations (Subscribers) asynchronously. 
Built-in Subscriber types include:
* **`LivestreamSubscriber`:** Maintains an in-memory GOP Cache to deliver Zero-delay frames to viewers via HTTP-FLV / WebSockets.
* **`HLSSubscriber`:** Slices streams into rolling `.flv` segments and generates Apple HLS standard m3u8 Playlists.
* **`MinIOSubscriber`:** Automatically archives and uploads video segments to S3/MinIO Cloud Servers in time blocks.
* **`StorageSubscriber`:** Seamlessly records the continuous stream as static files directly to local disk.
* **`RelaySubscriber`:** Forwards the raw Video stream directly to Satellite/Edge Nodes in a Cluster for Horizontal Scaling.

### 2. Zero-GC Performance Breakthrough (Reference Counting)
The server completely eliminates the overhead of the Go Garbage Collector (GC) within the packet transmission pipeline.
* All bytes of Video Frames are reused natively via **`sync.Pool`**.
* Integrates a rigid **Reference Counting** mechanism (`Retain()` and `Release()`), precisely mirroring C++'s `std::shared_ptr`, ensuring absolute memory safety without requiring the OS to allocate new memory.
* Saves tens of Gigabytes in RAM bandwidth and drastically reduces Syscalls.

### 3. Multi-Protocol Integration
* Directly pulls RTSP streams from Cameras via internal MediaMTX.
* Ready to convert RTMP into an ultra-lightweight native FLV-AVPacket struct (No dependency on CGO/FFmpeg for basic transmission layers).

### 4. ONVIF Hardware Control
Includes built-in ONVIF Adapters for Device Discovery, configuration retrieval, and PTZ (Pan-Tilt-Zoom) manipulation directly manipulating cameras using pure Go code.

### 5. High Availability (HA) & Idempotency
Cluster-ready design with deep Redis integration handling Distributed Locks, Camera Heartbeat monitoring, and Idempotent APIs ensuring smooth, non-duplicating device triggers.

---

## Project Structure

```text
go-cam-server/
├── cmd/server/          # Server Entrypoint
├── config/              # Configuration files (mediamtx.yml, server.yml)
├── internal/
│   ├── api/             # API Router (Go-chi) HTTP / WebSockets
│   ├── camera/          # Core Camera interface (ONVIF, RTSP Adapters)
│   ├── flv/             # Raw FLV Header Read/Write utilities
│   ├── ha/              # High Availability & Redis distributed locks
│   ├── minio/           # Cloud Storage interactions (S3 API Client)
│   ├── onvif/           # SOAP message payload constructions 
│   ├── rtsp/            # Ingester pulling streams from MediaMTX to Go Engine
│   ├── stream/          # The Core Pub/Sub Spiderweb (AVPacket, Fan-out) 🧠
│   └── subscriber/      # Output Logic Branches (HLS, Minio, Relay...)
├── pkg/                 # Shared utilities
├── docker-compose.yml   # Dev cluster with Redis & mock S3 setup
└── Makefile             # Utility commands for build, test, deploy
```

---

## Installation & Getting Started

### 1. Bootstrapping the Environment (Redis, MediaMTX)
Use Docker Compose to spin up the satellite services first:
```bash
docker-compose up -d
```

### 2. Running the Go Server
Ensure you have **Go 1.25.0** or higher installed.
```bash
# Download dependencies
go mod tidy

# Run the Live Stream server
go run cmd/server/main.go
```

*(Or simply use `make run` if you are on macOS / Linux).*

### 3. Building for Production
To build a compiled binary suitable for Production environments:
```bash
go build -o bin/server ./cmd/server
```

---

## Roadmap & Upcoming Plans
- [x] Core Pub/Sub Fan-out Architecture
- [x] Zero-GC buffer optimization using `sync.Pool`
- [ ] Migrate HTTP Streaming from standard `net/http` to Event-Loop Networking (`nbio`) to scale to +100k Concurrent Viewers.
- [ ] Add Cloud Relay cluster capabilities (Precise Origin-Edge Node communication pattern).

---
*Engineered for extreme I/O heat dissipation.*
