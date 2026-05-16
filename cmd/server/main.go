// go-cam-server — Go media gateway for MediaMTX ingest + fan-out.
//
// Replaces the C++ cam-server streaming layer with a pure-Go implementation
// using the publisher/subscriber pattern.
//
// Architecture:
//
//	Camera (RTMP push) ──► MediaMTX ──► RTSP pull ingest ──► stream.StreamManager
//	                                                          │
//	                          ┌───────────────────┼──────────────────────┐
//	                          ▼                   ▼                      ▼
//	                  StorageSubscriber     HLSSubscriber          (future)
//	                   ./storage/...        ./hls/...             RelaySubscriber
//	                   (FLV archive)       (m3u8 + segs)          (multi-node gRPC)
//
//	Viewer ──► GET /hls/{key}/index.m3u8 ──► ffplay / VLC
//	         GET /monitor/priority        ──► Flutter monitor grid
//
// Usage:
//
//	go run ./cmd/server                     # use defaults
//	go run ./cmd/server -config server.yml  # custom config
//
// Test camera push (to MediaMTX):
//
//	ffmpeg -re -i sample.mp4 -c copy -f flv rtmp://localhost:1935/cam1
//
// Test HLS playback:
//
//	ffplay http://localhost:8080/hls/cam1/index.m3u8
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"go-cam-server/config"
	"go-cam-server/internal/api"
	"go-cam-server/internal/auth"
	"go-cam-server/internal/camera"
	vmslogging "go-cam-server/internal/logging"
	vmsmediamtx "go-cam-server/internal/mediamtx"
	vmsmediamtxproc "go-cam-server/internal/mediamtxproc"
	vmsminio "go-cam-server/internal/minio"
	"go-cam-server/internal/node"
	"go-cam-server/internal/pionbridge"
	"go-cam-server/internal/relay"
	vmsredis "go-cam-server/internal/redis"
	vmsrtsp "go-cam-server/internal/rtsp"
	"go-cam-server/internal/stream"
	"go-cam-server/internal/subscriber"
)

func main() {
	cfgPath := flag.String("config", "server.yml", "path to config file")
	flag.Parse()

	// ─── Load config ──────────────────────────────────────────────────────────
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logrus.Fatalf("config: %v", err)
	}

	logCleanup, err := vmslogging.Setup(cfg.Logging)
	if err != nil {
		logrus.Fatalf("logging setup: %v", err)
	}
	defer logCleanup()

	logrus.Infof("starting node=%s role=%s", cfg.Node.ID, cfg.Node.Role)

	// ─── MediaMTX subprocess (optional) ──────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if cfg.MediaMTXProc.Enabled {
		mtxProc := vmsmediamtxproc.New(vmsmediamtxproc.Config{
			Enabled:      cfg.MediaMTXProc.Enabled,
			BinaryPath:   cfg.MediaMTXProc.BinaryPath,
			ConfigPath:   cfg.MediaMTXProc.ConfigPath,
			APIBaseURL:   cfg.MediaMTX.APIBaseURL,
			ReadyTimeout: time.Duration(cfg.MediaMTXProc.ReadyTimeout) * time.Second,
		})
		if err := mtxProc.Start(ctx); err != nil {
			logrus.Fatalf("mediamtxproc: %v", err)
		}
		defer mtxProc.Stop()
	}

	// ─── Core: StreamManager ─────────────────────────────────────────────────
	manager := stream.NewStreamManager()
	stopStats := startRuntimeStatsEmitter(cfg.Logging.StatIntervalSec, manager)
	defer close(stopStats)

	// ─── MinIO (optional) ─────────────────────────────────────────────────────
	minioClient := vmsminio.New(cfg.MinIO)
	subFactory := subscriber.NewFactory(minioClient, cfg.MinIO.SegmentDuration)
	mtxClient := vmsmediamtx.New(cfg.MediaMTX)
	var ingestMgr *vmsrtsp.IngestManager
	if cfg.MediaMTX.Enabled {
		ingestMgr = vmsrtsp.NewIngestManager(manager, subFactory, cfg)
	}
	pionService := pionbridge.New(cfg.PionWebRTC)
	if pionService != nil {
		defer pionService.Close()
	}

	// ─── Redis (optional) ─────────────────────────────────────────────────────
	rdb := vmsredis.NewClient(cfg)

	// ─── Node Registry ────────────────────────────────────────────────────────
	var registry *node.Registry
	if rdb != nil {
		registry = node.NewRegistry(rdb, cfg)
		registry.Start(func() int { return len(manager.All()) })
		defer registry.Stop()
		logrus.Infof("node-registry: started (redis=%s)", cfg.Redis.Addr)
	} else {
		logrus.Info("node-registry: disabled (no Redis)")
	}

	// Sync stream lifecycle events to Redis so peer nodes can locate streams.
	if registry != nil {
		manager.OnRegister = func(streamKey, _ string) {
			if err := registry.AnnounceStream(context.Background(), streamKey); err != nil {
				logrus.WithError(err).WithField("stream_key", streamKey).Warn("registry.announce_stream: failed")
			}
		}
		manager.OnUnregister = func(streamKey string) {
			if err := registry.RevokeStream(context.Background(), streamKey); err != nil {
				logrus.WithError(err).WithField("stream_key", streamKey).Warn("registry.revoke_stream: failed")
			}
		}
	}

	// ─── Auth + RBAC ─────────────────────────────────────────────────────────
	// L1: in-memory RBAC store — always present, O(1) permission checks.
	memRBAC := auth.NewInMemRBACStore()
	var rbacStore auth.RBACStore = memRBAC

	// L2: Redis RBAC store — persist + share across nodes when Redis available.
	if rdb != nil {
		redisRBAC := auth.NewRedisRBACStore(rdb)
		layered := auth.NewLayeredRBACStore(memRBAC, redisRBAC)
		if err := layered.WarmFrom(ctx, redisRBAC); err != nil {
			logrus.Warnf("rbac: warm from Redis failed: %v", err)
		}
		rbacStore = layered
	} else {
		logrus.Info("rbac: running in-memory only (no Redis)")
	}

	rsaKey := loadRSAKey(cfg.Auth)

	authStore := auth.NewStore()
	jwtSvc := auth.NewJWTService(rsaKey, cfg.Auth.TokenTTLHours)
	authSvc := auth.NewService(authStore, jwtSvc, rbacStore)

	if err := authSvc.SeedRBAC(ctx); err != nil {
		logrus.Fatalf("rbac.seed: %v", err)
	}
	if _, err := authSvc.SeedAdmin(ctx, cfg.Auth.SeedAdminUser, cfg.Auth.SeedAdminPass); err != nil {
		logrus.Fatalf("auth.seed: %v", err)
	}

	// ─── Camera Registry ─────────────────────────────────────────────────────
	tenantID, err := uuid.NewV7()
	if err != nil {
		logrus.Fatalf("camera: generate tenant_id: %v", err)
	}
	camStore := camera.NewStore()
	camSvc := camera.NewService(camStore, cfg.Node.ID, tenantID.String())

	// ─── Stream Engine ───────────────────────────────────────────────────────
	// Always created; ingestMgr may be nil (mediamtx disabled), in which case
	// EnsureStream returns a clear error instead of panicking.
	streamEngine := stream.NewEngine(manager, ingestMgr)

	// ─── Relay ───────────────────────────────────────────────────────────────
	relayTokenSvc := relay.NewTokenService(cfg.Relay.Secret)
	relayMgr := relay.NewManager(relayTokenSvc, cfg.Relay.BaseURL)
	relayMgr.StartReaper(time.Duration(cfg.Relay.IdleTimeout) * time.Second)

	// ─── HTTP API Server ──────────────────────────────────────────────────────
	apiServer := api.NewServer(cfg, api.Dependencies{
		Manager:      manager,
		Redis:        rdb,
		Registry:     registry,
		MediaMTX:     mtxClient,
		MinIO:        minioClient,
		Pion:         pionService,
		Subscribers:  subFactory,
		Ingest:       ingestMgr,
		Auth:         authSvc,
		StreamEngine: streamEngine,
		RelayManager: relayMgr,
		Camera:       camSvc,
	})

	// ─── Graceful shutdown ────────────────────────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)

	go func() {
		logrus.Infof("http: starting on %s", cfg.HTTP.Addr)
		errCh <- apiServer.ListenAndServe()
	}()

	// Wait for shutdown signal or fatal error
	select {
	case sig := <-sigCh:
		logrus.Infof("received signal %s, shutting down", sig)
	case err := <-errCh:
		logrus.Fatalf("fatal: %v", err)
	}

	if registry != nil {
		logrus.Info("shutdown: deregistering node")
		registry.Stop()
		_ = rdb.Shutdown(context.Background())
	}

	logrus.Info("shutdown complete")
}

// loadRSAKey resolves the RSA private key from config in priority order:
//  1. RSAPrivateKeyPath — read PEM from file (production)
//  2. RSAPrivateKeyPEM  — use inline PEM string (containers / env injection)
//  3. Neither set       — generate an ephemeral 2048-bit key (dev only, warn loudly)
func loadRSAKey(cfg config.AuthConfig) *rsa.PrivateKey {
	var pemBytes []byte

	switch {
	case cfg.RSAPrivateKeyPath != "":
		b, err := os.ReadFile(cfg.RSAPrivateKeyPath)
		if err != nil {
			logrus.Fatalf("auth: read RSA key file %q: %v", cfg.RSAPrivateKeyPath, err)
		}
		pemBytes = b

	case cfg.RSAPrivateKeyPEM != "":
		pemBytes = []byte(cfg.RSAPrivateKeyPEM)

	default:
		logrus.Warn("auth: no RSA key configured — generating ephemeral key (tokens invalid across restarts, dev only)")
		key, err := auth.GenerateRSAKey()
		if err != nil {
			logrus.Fatalf("auth: generate ephemeral RSA key: %v", err)
		}
		return key
	}

	block, _ := pem.Decode(pemBytes)
	if block == nil {
		logrus.Fatalf("auth: RSA key PEM is empty or malformed")
	}

	var key *rsa.PrivateKey
	var err error

	switch block.Type {
	case "RSA PRIVATE KEY": // PKCS#1
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY": // PKCS#8
		parsed, e := x509.ParsePKCS8PrivateKey(block.Bytes)
		if e != nil {
			logrus.Fatalf("auth: parse PKCS#8 key: %v", e)
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			logrus.Fatalf("auth: PKCS#8 key is not an RSA key")
		}
	default:
		logrus.Fatalf("auth: unsupported PEM block type %q (want RSA PRIVATE KEY or PRIVATE KEY)", block.Type)
	}

	if err != nil {
		logrus.Fatalf("auth: parse RSA key: %v", err)
	}
	if key.N.BitLen() < 2048 {
		logrus.Warnf("auth: RSA key is %d bits — 2048 minimum recommended", key.N.BitLen())
	}
	logrus.Infof("auth: loaded RSA key (%d bits)", key.N.BitLen())
	return key
}

func startRuntimeStatsEmitter(intervalSec int, manager *stream.StreamManager) chan struct{} {
	if intervalSec <= 0 {
		intervalSec = 30
	}

	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				streams := manager.All()
				totalSubscribers := 0
				for _, st := range streams {
					totalSubscribers += st.Stats().SubscriberCount
				}

				var mem runtime.MemStats
				runtime.ReadMemStats(&mem)

				vmslogging.Stat("runtime.stats", logrus.Fields{
					"stream_count":      len(streams),
					"subscriber_count":  totalSubscribers,
					"goroutines":        runtime.NumGoroutine(),
					"heap_alloc_bytes":  mem.Alloc,
					"heap_objects":      mem.HeapObjects,
					"gc_cycles":         mem.NumGC,
					"next_gc_bytes":     mem.NextGC,
					"total_alloc_bytes": mem.TotalAlloc,
				})
			case <-stop:
				return
			}
		}
	}()
	return stop
}
