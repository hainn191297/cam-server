#!/usr/bin/env bash
# demo.sh — End-to-end demo: 2 ffmpeg streams → go-cam-server (RTMP) → MinIO → playback URLs
#
# Usage:
#   bash scripts/demo.sh [path/to/sample.mp4]
#
# Requirements: ffmpeg, docker, go, jq
# Ports used: 1935 (RTMP), 8080 (API), 6379 (Redis), 9000/9001 (MinIO)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEMO_CONFIG="$REPO_ROOT/server.demo.yml"
SAMPLE="${1:-}"
FFMPEG_PID1="" FFMPEG_PID2="" SERVER_PID=""

# ─── Colors ──────────────────────────────────────────────────────────────────
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; RED='\033[0;31m'; NC='\033[0m'
info()  { echo -e "${GREEN}[demo]${NC} $*"; }
warn()  { echo -e "${YELLOW}[demo]${NC} $*"; }
error() { echo -e "${RED}[demo]${NC} $*" >&2; }
step()  { echo -e "\n${CYAN}══ $* ══${NC}"; }

# ─── Cleanup ─────────────────────────────────────────────────────────────────
cleanup() {
  echo ""
  info "Shutting down..."
  [[ -n "$FFMPEG_PID1" ]] && kill "$FFMPEG_PID1" 2>/dev/null || true
  [[ -n "$FFMPEG_PID2" ]] && kill "$FFMPEG_PID2" 2>/dev/null || true
  [[ -n "$SERVER_PID"  ]] && kill "$SERVER_PID"  2>/dev/null || true
  wait "$FFMPEG_PID1" "$FFMPEG_PID2" "$SERVER_PID" 2>/dev/null || true

  step "Stopping infrastructure"
  docker compose -f "$REPO_ROOT/docker-compose.yml" stop redis minio 2>/dev/null || true
  info "Done. Bye!"
}
trap cleanup EXIT INT TERM

# ─── 1. Check prerequisites ──────────────────────────────────────────────────
step "Checking prerequisites"

for cmd in ffmpeg docker go jq; do
  if ! command -v "$cmd" &>/dev/null; then
    error "Required command not found: $cmd"
    exit 1
  fi
  info "  ✓ $cmd $(${cmd} --version 2>&1 | head -1)"
done

if ! docker info &>/dev/null; then
  error "Docker daemon is not running."
  exit 1
fi

# ─── 2. Find / download sample video ─────────────────────────────────────────
step "Locating sample video"

if [[ -z "$SAMPLE" ]]; then
  # Try common locations
  for candidate in \
    "$REPO_ROOT/scripts/sample.mp4" \
    "$REPO_ROOT/sample.mp4" \
    "$HOME/Downloads/sample.mp4"
  do
    if [[ -f "$candidate" ]]; then
      SAMPLE="$candidate"
      break
    fi
  done
fi

if [[ -z "$SAMPLE" ]]; then
  SAMPLE="$REPO_ROOT/scripts/sample.mp4"
  warn "No sample video found. Downloading a 10-second Big Buck Bunny clip..."
  # 360p clip ~2 MB from a CDN mirror
  curl -fsSL -o "$SAMPLE" \
    "https://commondatastorage.googleapis.com/gtv-videos-bucket/sample/ForBiggerBlazes.mp4" \
    || { error "Download failed. Pass a video file as argument: bash scripts/demo.sh /path/to/video.mp4"; exit 1; }
  info "Downloaded: $SAMPLE"
fi

info "Using sample: $SAMPLE"

# ─── 3. Start infra (Redis + MinIO) ──────────────────────────────────────────
step "Starting infrastructure (Redis + MinIO)"

docker compose -f "$REPO_ROOT/docker-compose.yml" up redis minio -d --wait 2>&1 \
  | grep -E "(Started|Healthy|healthy|error)" || true

# Wait for MinIO to accept connections (max 30s)
info "Waiting for MinIO on localhost:9000..."
for i in $(seq 1 30); do
  if curl -sf http://localhost:9000/minio/health/live &>/dev/null; then
    info "  ✓ MinIO ready (${i}s)"
    break
  fi
  if [[ $i -eq 30 ]]; then
    error "MinIO did not become ready in 30s"
    exit 1
  fi
  sleep 1
done

# ─── 4. Build the Go server ───────────────────────────────────────────────────
step "Building go-cam-server"
(cd "$REPO_ROOT" && go build -o bin/server ./cmd/server)
info "Built: $REPO_ROOT/bin/server"

# ─── 5. Start the Go server ───────────────────────────────────────────────────
step "Starting go-cam-server (RTMP :1935, HTTP :8080)"
mkdir -p "$REPO_ROOT/logs" "$REPO_ROOT/hls" "$REPO_ROOT/storage"
"$REPO_ROOT/bin/server" -config "$DEMO_CONFIG" &>"$REPO_ROOT/logs/demo-server.log" &
SERVER_PID=$!
info "Server PID: $SERVER_PID (logs: logs/demo-server.log)"

# Wait for HTTP server (max 15s)
info "Waiting for HTTP server on :8080..."
for i in $(seq 1 15); do
  if curl -sf http://localhost:8080/streams &>/dev/null; then
    info "  ✓ Server ready (${i}s)"
    break
  fi
  if [[ $i -eq 15 ]]; then
    error "Server did not start in 15s. Check logs/demo-server.log"
    exit 1
  fi
  sleep 1
done

# ─── 6. Push 2 RTMP streams ──────────────────────────────────────────────────
step "Pushing 2 RTMP streams via ffmpeg"

ffmpeg -hide_banner -loglevel warning \
  -re -stream_loop -1 -i "$SAMPLE" \
  -c copy -f flv rtmp://localhost:1935/live/cam1 \
  &>"$REPO_ROOT/logs/ffmpeg-cam1.log" &
FFMPEG_PID1=$!
info "cam1 streaming (PID $FFMPEG_PID1, log: logs/ffmpeg-cam1.log)"

# Small delay so cam2 gets a slightly different offset
sleep 1

ffmpeg -hide_banner -loglevel warning \
  -re -stream_loop -1 -i "$SAMPLE" \
  -c copy -f flv rtmp://localhost:1935/live/cam2 \
  &>"$REPO_ROOT/logs/ffmpeg-cam2.log" &
FFMPEG_PID2=$!
info "cam2 streaming (PID $FFMPEG_PID2, log: logs/ffmpeg-cam2.log)"

# ─── 7. Wait for first MinIO segment ─────────────────────────────────────────
step "Waiting ~12s for the first MinIO segment to upload..."
sleep 12

# ─── 8. Show live stream status ───────────────────────────────────────────────
step "Live Streams  (GET /streams)"
curl -sf http://localhost:8080/streams | jq '.' || warn "No live streams yet"

step "Monitor Priority  (GET /monitor/priority)"
curl -sf http://localhost:8080/monitor/priority | jq '.' || true

# ─── 9. Show playback / recordings ───────────────────────────────────────────
step "Recorded Streams  (GET /playback/streams)"
curl -sf http://localhost:8080/playback/streams | jq '.' || warn "No recordings yet (MinIO segment_duration is 10s)"

step "cam1 Recordings + Presigned URLs  (GET /playback/cam1/recordings)"
curl -sf "http://localhost:8080/playback/cam1/recordings?limit=3" | jq '.' || warn "No recordings for cam1 yet"

step "cam2 Recordings + Presigned URLs  (GET /playback/cam2/recordings)"
curl -sf "http://localhost:8080/playback/cam2/recordings?limit=3" | jq '.' || warn "No recordings for cam2 yet"

# ─── 10. HLS playback hint ────────────────────────────────────────────────────
echo ""
info "HLS live stream available at:"
info "  cam1: http://localhost:8080/hls/cam1/index.m3u8"
info "  cam2: http://localhost:8080/hls/cam2/index.m3u8"
info ""
info "MinIO console: http://localhost:9001  (minioadmin / minioadmin)"
info "API base:      http://localhost:8080"
info ""
info "Demo is running. Recordings accumulate every 10s in MinIO."
info "Press Ctrl+C to stop everything."

# ─── 11. Keep running until Ctrl+C ───────────────────────────────────────────
# Poll every 15s and show a short status update
while true; do
  sleep 15
  echo ""
  info "── Status update ──"
  curl -sf http://localhost:8080/streams | jq '[.[] | {key, is_live, subscriber_count}]' 2>/dev/null || true
  curl -sf http://localhost:8080/playback/streams | jq '{recorded_streams: .streams}' 2>/dev/null || true
done
