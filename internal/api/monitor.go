package api

import (
	"encoding/json"
	"net/http"
	"sync"

	"go-cam-server/internal/stream"
)

// GET /monitor/priority
//
// Returns all live streams ranked by priority score (highest first).
// The Flutter monitor screen uses this to decide which cameras to show
// in N available grid slots.
//
// Example response:
//
//	{
//	  "weights": { "viewer_count": 0.4, ... },
//	  "streams": [
//	    { "stream_key": "cam-lobby", "score": 0.72, "viewers": 3, ... },
//	    { "stream_key": "cam-gate",  "score": 0.31, "viewers": 0, ... }
//	  ]
//	}
func (s *Server) monitorPriority(w http.ResponseWriter, r *http.Request) {
	streams := s.manager.All()
	weights := s.weightsStore.get()
	pins := s.pins.all()

	scores := stream.RankStreams(streams, weights, pins)

	writeJSON(w, http.StatusOK, map[string]any{
		"weights": weights,
		"streams": scores,
		"total":   len(scores),
	})
}

// PUT /monitor/weights
//
// Update priority scoring weights at runtime (no restart needed).
// Body: { "viewer_count": 0.5, "bitrate_health": 0.2, "recency": 0.1, "manual_pin": 0.1, "motion_activity": 0.1 }
func (s *Server) updateWeights(w http.ResponseWriter, r *http.Request) {
	var w2 stream.ScoringWeights
	if err := json.NewDecoder(r.Body).Decode(&w2); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.weightsStore.set(w2)
	writeJSON(w, http.StatusOK, map[string]any{"weights": w2})
}

// ─────────────────────────────────────────────────────────────────────────────
// pinStore: thread-safe set of pinned stream keys
// ─────────────────────────────────────────────────────────────────────────────

type pinStore struct {
	mu   sync.RWMutex
	keys map[string]bool
}

func newPinStore() *pinStore { return &pinStore{keys: make(map[string]bool)} }

func (p *pinStore) pin(key string) {
	p.mu.Lock()
	p.keys[key] = true
	p.mu.Unlock()
}

func (p *pinStore) unpin(key string) {
	p.mu.Lock()
	delete(p.keys, key)
	p.mu.Unlock()
}

func (p *pinStore) all() map[string]bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]bool, len(p.keys))
	for k, v := range p.keys {
		out[k] = v
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// weightsStore: thread-safe scoring weights
// ─────────────────────────────────────────────────────────────────────────────

type weightsStore struct {
	mu sync.RWMutex
	w  stream.ScoringWeights
}

func newWeightsStore() *weightsStore {
	return &weightsStore{w: stream.DefaultWeights}
}

func (ws *weightsStore) get() stream.ScoringWeights {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	return ws.w
}

func (ws *weightsStore) set(w stream.ScoringWeights) {
	ws.mu.Lock()
	ws.w = w
	ws.mu.Unlock()
}
