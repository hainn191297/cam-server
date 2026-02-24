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

	"github.com/sirupsen/logrus"

	"go-cam-server/config"
	"go-cam-server/internal/api"
	vmslogging "go-cam-server/internal/logging"
	vmsmediamtx "go-cam-server/internal/mediamtx"
	vmsmediamtxproc "go-cam-server/internal/mediamtxproc"
	vmsminio "go-cam-server/internal/minio"
	"go-cam-server/internal/node"
	"go-cam-server/internal/pionbridge"
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

	// Hook: when a stream is registered/unregistered, sync to Redis
	if registry != nil {
		// TODO: hook into StreamManager events to call registry.AnnounceStream
		// For now this is done by explicit control paths outside StreamManager.
		_ = registry
	}

	// ─── HTTP API Server ──────────────────────────────────────────────────────
	apiServer := api.NewServer(cfg, manager, rdb, registry, mtxClient, minioClient, pionService, subFactory, ingestMgr)

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
