package relay

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// Handler exposes the internal relay API for the control plane to call.
type Handler struct {
	mgr *Manager
}

func NewHandler(mgr *Manager) *Handler {
	return &Handler{mgr: mgr}
}

// RegisterRoutes mounts the internal relay API on a router.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Post("/internal/relay/sessions", h.CreateSession)
	r.Delete("/internal/relay/sessions/{relay_session_id}", h.CloseSession)
}

func (h *Handler) CreateSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"code":"bad_request"}`, http.StatusBadRequest)
		return
	}
	if req.TokenExpiresAt.IsZero() {
		req.TokenExpiresAt = time.Now().Add(8 * time.Hour)
	}

	resp, err := h.mgr.Create(req)
	if err != nil {
		http.Error(w, `{"code":"relay_unavailable"}`, http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) CloseSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "relay_session_id")
	h.mgr.Close(id)
	w.WriteHeader(http.StatusNoContent)
}
