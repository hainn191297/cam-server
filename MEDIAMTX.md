# MediaMTX + Pion WebRTC (No Auth Mode)

This project uses:
- `media_mtx` for ingest, HLS/RTSP/recorded playback
- `pion_webrtc` for live WebRTC signaling endpoint (`offer`/`answer`)

## 1) Run stack

```bash
cd go-cam-server
make up
```

`docker compose` now builds `mediamtx` from the local `third_party/mediamtx` subproject instead of pulling the upstream image directly. That keeps `go-cam-server` as the control plane while letting you patch and rebuild MediaMTX like a normal service.

Services exposed:
- `1935` RTMP ingest
- `8554` RTSP live
- `8888` HLS live
- `8889` WebRTC live
- `9996` Playback API
- `9997` MediaMTX API
- `8080` go-cam-server API

## 2) Push a stream

```bash
ffmpeg -re -f lavfi -i testsrc=size=1280x720:rate=25 \
  -c:v libx264 -pix_fmt yuv420p -preset veryfast -tune zerolatency \
  -f flv rtmp://localhost:1935/cam1
```

## 3) Live playback

- HLS: `http://localhost:8888/cam1/index.m3u8`
- Pion demo page: `http://localhost:8080/pion/webrtc/cam1/demo`
- Offer endpoint (for web/app): `POST http://localhost:8080/pion/webrtc/cam1/offer`

Get all URLs in one call:

```bash
curl -s "http://localhost:8080/live/cam1/urls" | jq
```

## 4) Playback (recorded)

- List segments from app API:

```bash
curl -s "http://localhost:8080/playback/cam1/timespans?limit=20" | jq
```

- Direct MediaMTX playback list:

```bash
curl -s "http://localhost:9996/list?path=cam1" | jq
```

## 5) App-facing helper APIs

- `GET /live/streams`
- `GET /live/{key}/urls`
- `POST /pion/webrtc/{key}/offer`
- `DELETE /pion/webrtc/session/{id}`
- `GET /playback/{key}/timespans`

## 6) Control-plane monitoring

- Lightweight dependency health:

```bash
curl -s "http://localhost:8080/control/health" | jq
```

- Detailed MediaMTX snapshot via `go-cam-server`:

```bash
curl -s "http://localhost:8080/control/media-mtx" | jq
```

Useful make targets:

```bash
make mediamtx-image
make mediamtx-logs
make test-control-health
make test-control-mediamtx
```

## 7) No-auth note

`mediamtx.yml` is configured without auth users on purpose (temporary mode).
Do not expose these ports directly to the public Internet until auth and edge protection are added.
