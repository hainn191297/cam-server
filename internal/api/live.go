package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// GET /live/streams
//
// Returns active MediaMTX paths with direct playback URLs for web/app clients.
func (s *Server) listLiveStreams(w http.ResponseWriter, r *http.Request) {
	type item struct {
		StreamKey      string `json:"stream_key"`
		Ready          bool   `json:"ready"`
		Source         string `json:"source,omitempty"`
		BytesReceived  uint64 `json:"bytes_received,omitempty"`
		HLSURL         string `json:"hls_url"`
		WebRTCURL      string `json:"webrtc_url"`
		WebRTCOfferURL string `json:"webrtc_offer_url,omitempty"`
		RTSPURL        string `json:"rtsp_url"`
		RTMPURL        string `json:"rtmp_url"`
	}

	if s.mtx == nil {
		streams := s.manager.All()
		resp := make([]item, 0, len(streams))
		for _, st := range streams {
			urls := s.resolveLiveURLs(st.Key())
			resp = append(resp, item{
				StreamKey:      st.Key(),
				Ready:          st.IsLive(),
				Source:         "local",
				HLSURL:         urls.HLS,
				WebRTCURL:      urls.WebRTC,
				WebRTCOfferURL: urls.WebRTCOffer,
				RTSPURL:        urls.RTSP,
				RTMPURL:        urls.RTMP,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"streams": resp,
			"total":   len(resp),
		})
		return
	}

	paths, err := s.mtx.ListPaths(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	resp := make([]item, 0, len(paths))
	for _, p := range paths {
		urls := s.resolveLiveURLs(p.Name)
		resp = append(resp, item{
			StreamKey:      p.Name,
			Ready:          p.Ready,
			Source:         p.Source,
			BytesReceived:  p.BytesReceived,
			HLSURL:         urls.HLS,
			WebRTCURL:      urls.WebRTC,
			WebRTCOfferURL: urls.WebRTCOffer,
			RTSPURL:        urls.RTSP,
			RTMPURL:        urls.RTMP,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"streams": resp,
		"total":   len(resp),
	})
}

// GET /live/{key}/urls
//
// Returns direct live URLs for one stream key.
func (s *Server) getLiveURLs(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "stream key is required"})
		return
	}

	if s.mtx == nil {
		if _, ok := s.manager.Get(key); !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "stream not found"})
			return
		}
	}

	urls := s.resolveLiveURLs(key)
	writeJSON(w, http.StatusOK, map[string]any{
		"stream_key":        key,
		"hls":               urls.HLS,
		"webrtc":            urls.WebRTC,
		"webrtc_offer_url":  urls.WebRTCOffer,
		"rtsp":              urls.RTSP,
		"rtmp":              urls.RTMP,
		"pion_demo_url":     "/pion/webrtc/" + key + "/demo",
		"pion_offer_method": http.MethodPost,
	})
}

// GET /playback/{key}/timespans
//
// Returns MediaMTX playback segments, each with a direct /get URL.
func (s *Server) listPlaybackTimespans(w http.ResponseWriter, r *http.Request) {
	if s.mtx == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "media_mtx is disabled or unavailable",
		})
		return
	}

	key := chi.URLParam(r, "key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "stream key is required"})
		return
	}

	segments, err := s.mtx.ListPlaybackSegments(r.Context(), key)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	availableTotal := len(segments)
	limit := parseLimit(r.URL.Query().Get("limit"), availableTotal)
	if limit < len(segments) {
		segments = segments[:limit]
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"stream_key":      key,
		"total":           len(segments),
		"available_total": availableTotal,
		"timespans":       segments,
	})
}
