package api

import (
	"math"
	"net/http"
	"time"
)

func (s *Server) controlHealth(w http.ResponseWriter, r *http.Request) {
	snapshot := s.healthMonitor.Snapshot(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"status":       snapshot.Status,
		"mode":         snapshot.Mode,
		"checked_at":   snapshot.CheckedAt,
		"dependencies": snapshot.Dependencies,
		"topology": map[string]string{
			"node_id": s.cfg.Node.ID,
			"region":  s.cfg.Topology.Region,
			"az":      s.cfg.Topology.AZ,
		},
	})
}

func (s *Server) controlMediaMTX(w http.ResponseWriter, r *http.Request) {
	if s.mtx == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "media_mtx is disabled or unavailable",
		})
		return
	}

	summary, err := s.mtx.Summary(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":            "up",
		"checked_at":        time.Now().UTC(),
		"path_count":        summary.PathCount,
		"ready_path_count":  summary.ReadyPathCount,
		"bytes_received":    summary.BytesReceived,
		"paths":             summary.Paths,
		"api_base_url":      s.cfg.MediaMTX.APIBaseURL,
		"playback_base_url": s.cfg.MediaMTX.PlaybackBaseURL,
	})
}

func (s *Server) controlSLO(w http.ResponseWriter, _ *http.Request) {
	apiTarget := s.cfg.SLO.APIAvailabilityTarget
	streamTarget := s.cfg.SLO.StreamStartSuccessTarget
	reconnectTarget := s.cfg.SLO.ReconnectSuccessTarget

	writeJSON(w, http.StatusOK, map[string]any{
		"slo": map[string]any{
			"api_availability_target":     apiTarget,
			"api_failure_budget_percent":  round2(100 - apiTarget),
			"stream_start_success_target": streamTarget,
			"stream_failure_budget":       round2(100 - streamTarget),
			"first_frame_p95_ms_target":   s.cfg.SLO.FirstFrameP95MsTarget,
			"reconnect_success_target":    reconnectTarget,
			"reconnect_failure_budget":    round2(100 - reconnectTarget),
		},
	})
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
