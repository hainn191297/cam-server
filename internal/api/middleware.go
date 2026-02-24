package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"

	vmslogging "go-cam-server/internal/logging"
)

func (s *Server) slowRequestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)

		elapsed := time.Since(start)
		fields := logrus.Fields{
			"method":      r.Method,
			"path":        r.URL.Path,
			"status":      sw.status,
			"request_id":  middleware.GetReqID(r.Context()),
			"remote_addr": r.RemoteAddr,
			"elapsed_ms":  elapsed.Milliseconds(),
		}
		logrus.WithFields(fields).Info("http.request")
		vmslogging.SlowIfExceeds("http.request", elapsed, fields)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
