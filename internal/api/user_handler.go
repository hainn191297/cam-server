package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"go-cam-server/internal/auth"
	"go-cam-server/internal/tracectx"
)

// POST /api/v1/users
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	tc, _ := tracectx.FromContext(r.Context())

	var req auth.CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON", tc.TraceID)
		return
	}

	detail, err := s.authSvc.CreateUser(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrUserAlreadyExists):
			writeJSONError(w, http.StatusConflict, "conflict", "username already exists", tc.TraceID)
		case errors.Is(err, auth.ErrRoleNotFound):
			writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error(), tc.TraceID)
		case errors.Is(err, auth.ErrInvalidCredentials):
			writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error(), tc.TraceID)
		default:
			writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error(), tc.TraceID)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(detail)
}

// GET /api/v1/users
func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users := s.authSvc.ListAllUsers(r.Context())
	if users == nil {
		users = []*auth.UserDetail{}
	}
	writeJSON(w, http.StatusOK, users)
}

// GET /api/v1/users/{user_id}
func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	tc, _ := tracectx.FromContext(r.Context())
	userID := chi.URLParam(r, "user_id")

	detail, err := s.authSvc.GetUser(r.Context(), userID)
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "user not found", tc.TraceID)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error(), tc.TraceID)
		return
	}

	writeJSON(w, http.StatusOK, detail)
}

// DELETE /api/v1/users/{user_id}
func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	tc, _ := tracectx.FromContext(r.Context())
	userID := chi.URLParam(r, "user_id")

	if err := s.authSvc.DeleteUser(r.Context(), userID); err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "user not found", tc.TraceID)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error(), tc.TraceID)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// PATCH /api/v1/users/{user_id}/roles
func (s *Server) updateUserRoles(w http.ResponseWriter, r *http.Request) {
	tc, _ := tracectx.FromContext(r.Context())
	userID := chi.URLParam(r, "user_id")

	var req auth.UpdateUserRolesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON", tc.TraceID)
		return
	}
	if len(req.Roles) == 0 {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "roles must not be empty", tc.TraceID)
		return
	}

	detail, err := s.authSvc.UpdateUserRoles(r.Context(), userID, req.Roles)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrUserNotFound):
			writeJSONError(w, http.StatusNotFound, "not_found", "user not found", tc.TraceID)
		case errors.Is(err, auth.ErrRoleNotFound):
			writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error(), tc.TraceID)
		default:
			writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error(), tc.TraceID)
		}
		return
	}

	writeJSON(w, http.StatusOK, detail)
}
