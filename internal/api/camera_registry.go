package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"go-cam-server/internal/camera"
	"go-cam-server/internal/tracectx"
)

func (s *Server) createCamera(w http.ResponseWriter, r *http.Request) {
	tc, _ := tracectx.FromContext(r.Context())

	var body camera.CamWrite
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON", tc.TraceID)
		return
	}

	cam, err := s.camSvc.Create(body)
	if err != nil {
		var valErr camera.ErrCamValidation
		if errors.As(err, &valErr) {
			writeJSONError(w, http.StatusBadRequest, "validation_error", valErr.Err.Error(), tc.TraceID)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error(), tc.TraceID)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(cam)
}

func (s *Server) listCameras(w http.ResponseWriter, r *http.Request) {
	cams := s.camSvc.List()
	if cams == nil {
		cams = []*camera.Cam{}
	}
	writeJSON(w, http.StatusOK, cams)
}

func (s *Server) getCamera(w http.ResponseWriter, r *http.Request) {
	tc, _ := tracectx.FromContext(r.Context())
	camID := chi.URLParam(r, "cam_id")

	cam, err := s.camSvc.Get(camID)
	if err != nil {
		if errors.Is(err, camera.ErrCamNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "camera not found", tc.TraceID)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error(), tc.TraceID)
		return
	}

	writeJSON(w, http.StatusOK, cam)
}

func (s *Server) deleteCamera(w http.ResponseWriter, r *http.Request) {
	tc, _ := tracectx.FromContext(r.Context())
	camID := chi.URLParam(r, "cam_id")

	if err := s.camSvc.Delete(camID); err != nil {
		if errors.Is(err, camera.ErrCamNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "camera not found", tc.TraceID)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error(), tc.TraceID)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
