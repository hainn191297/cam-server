package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// GET /playback/streams
//
// Returns distinct stream keys that already have recording segments in MinIO.
func (s *Server) listRecordingStreams(w http.ResponseWriter, r *http.Request) {
	if s.minio == nil {
		if s.mtx != nil {
			paths, err := s.mtx.ListPaths(r.Context())
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
				return
			}
			streams := make([]string, 0, len(paths))
			for _, p := range paths {
				streams = append(streams, p.Name)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"streams": streams,
				"total":   len(streams),
				"source":  "mediamtx",
			})
			return
		}

		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "minio is disabled or unavailable",
		})
		return
	}

	keys, err := s.minio.AllStreamKeys(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"streams": keys,
		"total":   len(keys),
	})
}

// GET /playback/{key}/recordings
//
// Query:
//   - limit (optional): max number of items to return, defaults to all
//
// Response includes presigned_url per recording object.
func (s *Server) listRecordings(w http.ResponseWriter, r *http.Request) {
	if s.minio == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "minio is disabled or unavailable; use /playback/{key}/timespans for mediamtx playback",
		})
		return
	}

	key := chi.URLParam(r, "key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "stream key is required"})
		return
	}

	recordings, err := s.minio.ListRecordings(r.Context(), key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	limit := parseLimit(r.URL.Query().Get("limit"), len(recordings))
	availableTotal := len(recordings)
	if limit < len(recordings) {
		recordings = recordings[:limit]
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"stream_key":      key,
		"total":           len(recordings),
		"available_total": availableTotal,
		"recordings":      recordings,
	})
}

func parseLimit(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return fallback
	}
	if limit > fallback {
		return fallback
	}
	return limit
}
