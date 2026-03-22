.PHONY: run run-config build tidy test clean proto-gen up down logs mediamtx-image mediamtx-logs test-push test-push-lavfi test-play test-api test-live-streams test-live-urls test-playback-timespans test-playback-streams test-playback test-control-health test-control-mediamtx mediamtx-build mediamtx-update

# Run the server with defaults
run:
	go run ./cmd/server

# Run with custom config
run-config:
	go run ./cmd/server -config server.yml

# Start local stack (server + MediaMTX + MinIO + Redis)
up:
	docker compose up --build -d

# Stop local stack
down:
	docker compose down

# Tail server logs
logs:
	docker compose logs -f server

# Build the local custom MediaMTX image from third_party/mediamtx
mediamtx-image:
	docker compose build mediamtx

# Tail MediaMTX logs
mediamtx-logs:
	docker compose logs -f mediamtx

# Build binary
build:
	go build -o bin/go-cam-server ./cmd/server

# Build MediaMTX binary from the git submodule (third_party/mediamtx)
mediamtx-build:
	cd third_party/mediamtx && go build -o ../../mediamtx .

# Pull latest changes from MediaMTX upstream
mediamtx-update:
	git submodule update --remote third_party/mediamtx

# Tidy modules
tidy:
	go mod tidy

# Run tests
test:
	go test ./...

# Clean build artifacts and stream output
clean:
	rm -rf bin/ hls/ storage/

# Generate protobuf (future: gRPC inter-node relay)
# Requires: go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#           go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
proto-gen:
	protoc --go_out=. --go_opt=paths=source_relative \
	       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
	       proto/media.proto

# ─── Test helpers ─────────────────────────────────────────────────────────────

# Push a test stream into MediaMTX (requires ffmpeg + sample.mp4)
test-push:
	ffmpeg -re -i sample.mp4 -c copy -f flv rtmp://localhost:1935/cam1

# Push a synthetic test stream into MediaMTX (no sample file required)
test-push-lavfi:
	ffmpeg -re -f lavfi -i testsrc=size=1280x720:rate=25 -c:v libx264 -pix_fmt yuv420p -preset veryfast -tune zerolatency -f flv rtmp://localhost:1935/cam1

# Watch live via MediaMTX HLS (requires ffplay)
test-play:
	ffplay http://localhost:8888/cam1/index.m3u8

# Check stream list
test-api:
	curl -s http://localhost:8080/streams | jq
	curl -s http://localhost:8080/live/streams | jq
	curl -s http://localhost:8080/monitor/priority | jq
	curl -s http://localhost:8080/nodes | jq

# Control-plane dependency health
test-control-health:
	curl -s http://localhost:8080/control/health | jq

# Detailed MediaMTX status via go-cam-server
test-control-mediamtx:
	curl -s http://localhost:8080/control/media-mtx | jq

# Live API: list MediaMTX streams with direct URLs
test-live-streams:
	curl -s http://localhost:8080/live/streams | jq

# Live API: get direct URLs for one stream
test-live-urls:
	curl -s http://localhost:8080/live/cam1/urls | jq

# Playback API: list recorded timespans from MediaMTX
test-playback-timespans:
	curl -s "http://localhost:8080/playback/cam1/timespans?limit=20" | jq

# Playback API: list stream keys that have MinIO recordings
test-playback-streams:
	curl -s http://localhost:8080/playback/streams | jq

# Playback API: list recording objects + presigned URLs for one stream
test-playback:
	curl -s "http://localhost:8080/playback/cam1/recordings?limit=20" | jq

# Run load test script (default 10 cams: make load-test CAMS=20)
load-test:
	chmod +x scripts/load_test.sh
	./scripts/load_test.sh $(CAMS)
