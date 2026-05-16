package auth

import (
	"encoding/json"
	"net/http"

	"github.com/sirupsen/logrus"
)

// Handler groups the auth HTTP handlers.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Login handles POST /api/v1/auth/login
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAuthError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeAuthError(w, http.StatusBadRequest, "bad_request", "username and password required")
		return
	}

	resp, _, err := h.svc.Login(r.Context(), req)
	if err != nil {
		switch err {
		case ErrInvalidCredentials:
			writeAuthError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		case ErrAccountDisabled:
			writeAuthError(w, http.StatusForbidden, "account_disabled", "account is disabled")
		default:
			logrus.WithError(err).Error("auth.login: internal error")
			writeAuthError(w, http.StatusInternalServerError, "internal_error", "login failed")
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// Logout handles DELETE /api/v1/auth/session
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	sess, ok := SessionFromContext(r.Context())
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.svc.Logout(r.Context(), sess.TokenID)
	w.WriteHeader(http.StatusNoContent)
}

// SessionInfo handles GET /api/v1/auth/session
func (h *Handler) SessionInfo(w http.ResponseWriter, r *http.Request) {
	sess, _ := SessionFromContext(r.Context())
	u, _ := UserFromContext(r.Context())
	info := h.svc.GetSessionInfo(sess, u)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}
