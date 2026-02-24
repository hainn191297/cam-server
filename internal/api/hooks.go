package api

import (
	"net/http"

	"github.com/sirupsen/logrus"
)

// POST /internal/on-publish?path={key}
//
// Called by MediaMTX runOnPublish hook when a camera starts publishing.
// Triggers RTSP pull ingestion of the stream from MediaMTX.
// Returns 200 immediately; ingestion runs asynchronously.
func (s *Server) onPublish(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("path")
	if key == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	if s.ingest == nil {
		// Ingestion disabled (no MediaMTX config).
		w.WriteHeader(http.StatusOK)
		return
	}
	logrus.Infof("hook: on-publish key=%s src=%s", key, r.RemoteAddr)
	if err := s.ingest.Start(r.Context(), key); err != nil {
		logrus.Errorf("hook: on-publish[%s]: start ingest: %v", key, err)
		http.Error(w, "ingest start failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// POST /internal/on-unpublish?path={key}
//
// Called by MediaMTX runOnUnpublish hook when a camera stops publishing.
// Stops the RTSP pull ingestion for the stream.
func (s *Server) onUnpublish(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("path")
	if key == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	if s.ingest == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	logrus.Infof("hook: on-unpublish key=%s src=%s", key, r.RemoteAddr)
	s.ingest.Stop(key)
	w.WriteHeader(http.StatusOK)
}
