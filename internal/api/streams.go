package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"

	"go-cam-server/internal/stream"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response types
// ─────────────────────────────────────────────────────────────────────────────

type streamResponse struct {
	Key             string    `json:"key"`
	NodeID          string    `json:"node_id"`
	IsLive          bool      `json:"is_live"`
	StartedAt       time.Time `json:"started_at"`
	SubscriberCount int       `json:"subscriber_count"`
	PacketsTotal    uint64    `json:"packets_total"`
	HLSUrl          string    `json:"hls_url"`
	WebRTCUrl       string    `json:"webrtc_url,omitempty"`
	WebRTCOfferURL  string    `json:"webrtc_offer_url,omitempty"`
	Subscribers     []subInfo `json:"subscribers,omitempty"`
}

type subInfo struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Dropped uint64 `json:"dropped_packets"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Handlers
// ─────────────────────────────────────────────────────────────────────────────

// GET /streams
func (s *Server) listStreams(w http.ResponseWriter, r *http.Request) {
	streams := s.manager.All()
	if len(streams) == 0 && s.mtx != nil {
		paths, err := s.mtx.ListPaths(r.Context())
		if err == nil {
			resp := make([]streamResponse, 0, len(paths))
			for _, p := range paths {
				if !p.Ready {
					continue
				}
				urls := s.resolveLiveURLs(p.Name)
				resp = append(resp, streamResponse{
					Key:            p.Name,
					NodeID:         "mediamtx",
					IsLive:         p.Ready,
					StartedAt:      time.Time{},
					HLSUrl:         urls.HLS,
					WebRTCUrl:      urls.WebRTC,
					WebRTCOfferURL: urls.WebRTCOffer,
				})
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}

	resp := make([]streamResponse, 0, len(streams))
	for _, st := range streams {
		resp = append(resp, s.toStreamResponse(st))
	}
	writeJSON(w, http.StatusOK, resp)
}

// GET /streams/{key}
func (s *Server) getStream(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	st, ok := s.manager.Get(key)
	if !ok {
		if s.mtx != nil {
			paths, err := s.mtx.ListPaths(r.Context())
			if err == nil {
				for _, p := range paths {
					if p.Name == key && p.Ready {
						urls := s.resolveLiveURLs(p.Name)
						writeJSON(w, http.StatusOK, streamResponse{
							Key:            p.Name,
							NodeID:         "mediamtx",
							IsLive:         true,
							StartedAt:      time.Time{},
							HLSUrl:         urls.HLS,
							WebRTCUrl:      urls.WebRTC,
							WebRTCOfferURL: urls.WebRTCOffer,
						})
						return
					}
				}
			}
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "stream not found"})
		return
	}
	resp := s.toStreamResponse(st)
	for _, sub := range st.Subscribers() {
		resp.Subscribers = append(resp.Subscribers, subInfo{
			ID:      sub.ID(),
			Type:    sub.Type().String(),
			Dropped: sub.DroppedPackets(),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// POST /streams/{key}/pin
func (s *Server) pinStream(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	s.pins.pin(key)
	writeJSON(w, http.StatusOK, map[string]string{"status": "pinned", "key": key})
}

// DELETE /streams/{key}/pin
func (s *Server) unpinStream(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	s.pins.unpin(key)
	writeJSON(w, http.StatusOK, map[string]string{"status": "unpinned", "key": key})
}

// GET /hls/{key}/index.m3u8
//
// Multi-node logic:
//  1. If local HLS segments exist → serve directly (fast path)
//  2. If stream is local but HLS not ready yet → 503 (still starting)
//  3. If stream is on another node → trigger relay pull, then redirect viewer
//     to the local HLS once the relay has generated at least one segment
func (s *Server) serveHLSPlaylist(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	localPath := filepath.Join(s.cfg.HLS.RootPath, key, "index.m3u8")

	// Fast path: local HLS file already exists
	if _, err := os.Stat(localPath); err == nil {
		w.Header().Set("Content-Type", "application/x-mpegURL")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, localPath)
		return
	}

	// Stream is live locally but HLS not ready yet (< first segment duration)
	if _, ok := s.manager.Get(key); ok {
		http.Error(w, "stream starting, retry in 2s", http.StatusServiceUnavailable)
		return
	}

	// Stream not on this node — check cluster
	if s.registry == nil || s.relay == nil {
		http.Error(w, "stream not found", http.StatusNotFound)
		return
	}

	sourceNodeID, err := s.registry.FindStream(r.Context(), key)
	if err != nil {
		http.Error(w, "stream not found in cluster", http.StatusNotFound)
		return
	}

	if sourceNodeID == s.registry.SelfID() {
		// Stream is ours but HLS isn't ready yet
		http.Error(w, "stream starting, retry in 2s", http.StatusServiceUnavailable)
		return
	}

	// Start relay pull from source node (idempotent: safe to call multiple times)
	if err := s.relay.EnsureRelay(r.Context(), key, sourceNodeID); err != nil {
		http.Error(w, "relay unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Relay is starting — redirect viewer back to ourselves after a brief wait.
	// The HLS subscriber attached to the relay will generate segments shortly.
	// 307 Temporary Redirect so the client retries the same URL.
	w.Header().Set("Retry-After", "2")
	http.Redirect(w, r, "/hls/"+key+"/index.m3u8", http.StatusTemporaryRedirect)
}

// GET /hls/{key}/{file}  (segment files)
func (s *Server) serveHLSSegment(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	file := chi.URLParam(r, "file")
	path := filepath.Join(s.cfg.HLS.RootPath, key, filepath.Base(file))
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, path)
}

// GET /storage/{key}/{file}  (playback FLV archive)
func (s *Server) serveStorage(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	file := chi.URLParam(r, "file")
	path := filepath.Join(s.cfg.Storage.RootPath, key, filepath.Base(file))

	if _, err := os.Stat(path); os.IsNotExist(err) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found"})
		return
	}

	w.Header().Set("Content-Type", "video/x-flv")
	http.ServeFile(w, r, path)
}

// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) toStreamResponse(st stream.Stream) streamResponse {
	stats := st.Stats()
	urls := s.resolveLiveURLs(st.Key())

	return streamResponse{
		Key:             st.Key(),
		NodeID:          st.Publisher().NodeID(),
		IsLive:          st.IsLive(),
		StartedAt:       st.Publisher().StartedAt(),
		SubscriberCount: stats.SubscriberCount,
		PacketsTotal:    stats.PacketsTotal,
		HLSUrl:          urls.HLS,
		WebRTCUrl:       urls.WebRTC,
		WebRTCOfferURL:  urls.WebRTCOffer,
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
