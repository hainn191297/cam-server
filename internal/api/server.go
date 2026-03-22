// Package api provides the HTTP management API.
//
// Routes:
//
//	GET  /streams                    — list all live streams (local + relayed)
//	GET  /streams/:key               — stream detail + subscriber list
//	POST /streams/:key/pin           — pin a stream (monitor priority)
//	DELETE /streams/:key/pin         — unpin a stream
//	GET  /hls/:key/index.m3u8        — serve HLS playlist; auto-relay if on another node
//	GET  /hls/:key/:file             — serve HLS segment file
//	GET  /relay/:key                 — (inter-node) stream live FLV to a peer node
//	GET  /storage/:key/:file         — serve recorded FLV file (playback)
//	GET  /playback/streams           — list stream keys that have recordings in MinIO
//	GET  /playback/:key/recordings   — list recording segments + presigned URLs
//	GET  /live/streams               — list active paths from MediaMTX
//	GET  /live/:key/urls             — direct live URLs (HLS/WebRTC/RTSP/RTMP)
//	POST /live/:key/session          — create reconnect lease for web/app
//	POST /live/session/reattach      — renew lease from reattach token
//	POST /pion/webrtc/:key/offer     — create WebRTC answer from browser/app offer (no auth)
//	DELETE /pion/webrtc/session/:id  — close one Pion WebRTC session
//	GET  /pion/webrtc/:key/demo      — demo HTML page that negotiates with the offer endpoint
//	GET  /playback/:key/timespans    — list MediaMTX playback timespans
//	GET  /control/health             — control-plane dependency health & mode
//	GET  /control/media-mtx          — detailed MediaMTX status from control-plane
//	GET  /control/slo                — current SLO targets/failure budgets
//	GET  /nodes                      — list cluster nodes from Redis
//	GET  /monitor/priority           — ranked stream list for monitor grid
//	PUT  /monitor/weights            — update scoring weights at runtime
package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"

	"go-cam-server/config"
	"go-cam-server/internal/ha"
	vmsmediamtx "go-cam-server/internal/mediamtx"
	vmsminio "go-cam-server/internal/minio"
	"go-cam-server/internal/node"
	"go-cam-server/internal/pionbridge"
	vmsrtsp "go-cam-server/internal/rtsp"
	"go-cam-server/internal/stream"
	"go-cam-server/internal/subscriber"
	"go-cam-server/internal/tracectx"
)

type Server struct {
	cfg      *config.Config
	manager  *stream.StreamManager
	rdb      *redis.Client
	registry *node.Registry
	relay    *node.RelayManager // nil when Redis is not configured
	mtx      *vmsmediamtx.Client
	minio    *vmsminio.Client
	pion     *pionbridge.Service
	ingest   *vmsrtsp.IngestManager // nil when MediaMTX ingest is disabled
	router   *chi.Mux

	healthMonitor *ha.Monitor
	idempotency   *ha.IdempotencyStore
	leaseService  *ha.LeaseService

	pins         *pinStore
	weightsStore *weightsStore
}

type Dependencies struct {
	Manager     *stream.StreamManager
	Redis       *redis.Client
	Registry    *node.Registry
	MediaMTX    *vmsmediamtx.Client
	MinIO       *vmsminio.Client
	Pion        *pionbridge.Service
	Subscribers *subscriber.Factory
	Ingest      *vmsrtsp.IngestManager
}

func NewServer(cfg *config.Config, deps Dependencies) *Server {
	manager := deps.Manager
	rdb := deps.Redis
	registry := deps.Registry
	mediamtxClient := deps.MediaMTX
	minioClient := deps.MinIO
	pionService := deps.Pion
	subFactory := deps.Subscribers
	ingestMgr := deps.Ingest

	if subFactory == nil {
		subFactory = subscriber.NewFactory(minioClient, cfg.MinIO.SegmentDuration)
	}

	var relayMgr *node.RelayManager
	if registry != nil {
		relayMgr = node.NewRelayManager(registry, manager, func(key string, tc tracectx.Context) []stream.Subscriber {
			return subFactory.RelaySubscribers(key, cfg, tc)
		})
	}

	healthDeps := buildDependencies(rdb, mediamtxClient, minioClient)
	dependencyTimeout := time.Duration(cfg.HA.DependencyTimeoutMs) * time.Millisecond
	idempotencyTTL := time.Duration(cfg.HA.IdempotencyTTLSeconds) * time.Second
	leaseTTL := time.Duration(cfg.HA.LeaseTTLSeconds) * time.Second
	leaseSkew := time.Duration(cfg.HA.LeaseClockSkewSeconds) * time.Second

	s := &Server{
		cfg:           cfg,
		manager:       manager,
		rdb:           rdb,
		registry:      registry,
		relay:         relayMgr,
		mtx:           mediamtxClient,
		minio:         minioClient,
		pion:          pionService,
		ingest:        ingestMgr,
		healthMonitor: ha.NewMonitor(dependencyTimeout, healthDeps),
		idempotency:   ha.NewIdempotencyStore(idempotencyTTL, cfg.HA.IdempotencyMaxKeys),
		leaseService:  ha.NewLeaseService(rdb, leaseTTL, leaseSkew, cfg.HA.LeaseTokenSecret),
		pins:          newPinStore(),
		weightsStore:  newWeightsStore(),
	}
	s.router = s.buildRouter()
	return s
}

func (s *Server) ListenAndServe() error {
	return http.ListenAndServe(s.cfg.HTTP.Addr, s.router)
}

func (s *Server) buildRouter() *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(s.traceMiddleware)
	r.Use(s.slowRequestMiddleware)
	r.Use(middleware.Recoverer)

	// Mount pprof profiling endpoints
	r.Mount("/debug", middleware.Profiler())

	// ── MediaMTX ingest webhooks (called by MediaMTX ready/not-ready hooks) ───
	r.Post("/internal/on-publish", s.onPublish)
	r.Post("/internal/on-unpublish", s.onUnpublish)

	// ── Inter-node relay (raw FLV stream, no JSON content-type) ──────────────
	r.Get("/relay/{key}", s.serveRelay)

	// ── HLS & Storage (binary/static, no JSON content-type) ──────────────────
	r.Get("/hls/{key}/index.m3u8", s.serveHLSPlaylist)
	r.Get("/hls/{key}/{file}", s.serveHLSSegment)
	r.Get("/storage/{key}/{file}", s.serveStorage)
	r.Get("/pion/webrtc/{key}/demo", s.pionDemoPage)

	// ── JSON API ──────────────────────────────────────────────────────────────
	r.Group(func(r chi.Router) {
		r.Use(jsonContentType)

		r.Get("/control/health", s.controlHealth)
		r.Get("/control/media-mtx", s.controlMediaMTX)
		r.Get("/control/slo", s.controlSLO)
		r.Get("/streams", s.listStreams)
		r.Get("/streams/{key}", s.getStream)
		r.Post("/streams/{key}/pin", s.pinStream)
		r.Delete("/streams/{key}/pin", s.unpinStream)
		r.Get("/live/streams", s.listLiveStreams)
		r.Get("/live/{key}/urls", s.getLiveURLs)
		r.Post("/live/{key}/session", s.createLiveSession)
		r.Post("/live/session/reattach", s.reattachLiveSession)
		r.Post("/pion/webrtc/{key}/offer", s.pionOffer)
		r.Delete("/pion/webrtc/session/{id}", s.pionCloseSession)
		r.Get("/playback/streams", s.listRecordingStreams)
		r.Get("/playback/{key}/recordings", s.listRecordings)
		r.Get("/playback/{key}/timespans", s.listPlaybackTimespans)

		r.Get("/nodes", s.listNodes)

		r.Get("/monitor/priority", s.monitorPriority)
		r.Put("/monitor/weights", s.updateWeights)

		// ── Camera adapter API (ONVIF, manual, future adapters) ─────────────
		r.Get("/onvif/discover", s.onvifDiscover)
		r.Get("/onvif/info", s.onvifGetInfo)
		r.Get("/onvif/snapshot", s.onvifSnapshot)
		r.Post("/onvif/ptz", s.onvifPTZ)
		r.Post("/onvif/reboot", s.onvifReboot)
		r.Post("/onvif/resolve", s.onvifResolve)
		r.Post("/camera/resolve", s.cameraResolve)
	})

	return r
}

func buildDependencies(
	rdb *redis.Client,
	mtx *vmsmediamtx.Client,
	minioClient *vmsminio.Client,
) []ha.Dependency {
	out := make([]ha.Dependency, 0, 3)
	if rdb != nil {
		out = append(out, ha.Dependency{
			Name:     "redis",
			Required: true,
			Check: func(ctx context.Context) error {
				return rdb.Ping(ctx).Err()
			},
		})
	}
	if mtx != nil {
		out = append(out, ha.Dependency{
			Name:     "media_mtx",
			Required: true,
			Check: func(ctx context.Context) error {
				return mtx.Health(ctx)
			},
		})
	}
	if minioClient != nil {
		out = append(out, ha.Dependency{
			Name:     "minio",
			Required: false,
			Check: func(ctx context.Context) error {
				return minioClient.Health(ctx)
			},
		})
	}
	return out
}

func jsonContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}
