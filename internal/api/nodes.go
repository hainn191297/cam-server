package api

import (
	"net/http"
)

// GET /nodes — list all live cluster nodes from Redis
func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"nodes": []any{},
			"note":  "Redis not configured — single-node mode",
		})
		return
	}

	nodes, err := s.registry.AllNodes(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}
