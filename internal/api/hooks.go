package api

import (
	"net/http"

	"github.com/sirupsen/logrus"

	"go-cam-server/internal/tracectx"
)

// POST /internal/on-publish?path={key}
//
// Called by MediaMTX runOnReady hook when a stream becomes available.
// Triggers RTSP pull ingestion of the stream from MediaMTX.
// Returns 200 immediately; ingestion runs asynchronously.
func (s *Server) onPublish(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("path")
	if key == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if _, ok := tracectx.FromContext(ctx); !ok {
		ctx = tracectx.WithContext(ctx, tracectx.ExtractOrNew(r))
	}
	fields := tracectx.Fields(ctx)
	fields["stream_key"] = key
	fields["source_type"] = r.URL.Query().Get("source_type")
	fields["source_id"] = r.URL.Query().Get("source_id")
	fields["remote_addr"] = r.RemoteAddr
	if s.ingest == nil {
		// Ingestion disabled (no MediaMTX config).
		logrus.WithFields(fields).Info("hook.on_publish.skip_ingest_disabled")
		w.WriteHeader(http.StatusOK)
		return
	}
	logrus.WithFields(fields).Info("hook.on_publish")
	if err := s.ingest.Start(ctx, key); err != nil {
		logrus.WithFields(fields).WithError(err).Error("hook.on_publish.start_ingest_failed")
		http.Error(w, "ingest start failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// POST /internal/on-unpublish?path={key}
//
// Called by MediaMTX runOnNotReady hook when a stream is not available anymore.
// Stops the RTSP pull ingestion for the stream.
func (s *Server) onUnpublish(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("path")
	if key == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if _, ok := tracectx.FromContext(ctx); !ok {
		ctx = tracectx.WithContext(ctx, tracectx.ExtractOrNew(r))
	}
	fields := tracectx.Fields(ctx)
	fields["stream_key"] = key
	fields["source_type"] = r.URL.Query().Get("source_type")
	fields["source_id"] = r.URL.Query().Get("source_id")
	fields["remote_addr"] = r.RemoteAddr
	if s.ingest == nil {
		logrus.WithFields(fields).Info("hook.on_unpublish.skip_ingest_disabled")
		w.WriteHeader(http.StatusOK)
		return
	}
	logrus.WithFields(fields).Info("hook.on_unpublish")
	s.ingest.Stop(key)
	w.WriteHeader(http.StatusOK)
}
