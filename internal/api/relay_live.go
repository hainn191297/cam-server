package api

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"

	"go-cam-server/internal/relay"
	"go-cam-server/internal/tracectx"
)

// serveRelayLiveStream handles GET /relay/live/{protocol}/{relay_session_id}.
//
// Flow:
//  1. Extract and verify relay JWT from Authorization header.
//  2. Look up the live relay session to get cam_id + profile_id + negotiated protocol.
//  3. Serve the stream appropriate for the protocol:
//     - hls     → serve the HLS playlist file directly from disk.
//     - webrtc  → return the Pion offer URL for the client to POST an SDP offer.
func (s *Server) serveRelayLiveStream(w http.ResponseWriter, r *http.Request) {
	tc, _ := tracectx.FromContext(r.Context())

	protocol := chi.URLParam(r, "protocol")
	relaySessionID := chi.URLParam(r, "relay_session_id")

	// Verify relay JWT
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "missing relay token", tc.TraceID)
		return
	}
	signed := strings.TrimPrefix(authHeader, "Bearer ")

	if s.relayManager == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "relay_unavailable", "relay not configured", tc.TraceID)
		return
	}

	sess, err := s.relayManager.VerifyAndGetSession(signed, relaySessionID)
	if err != nil {
		logrus.WithError(err).WithField("relay_session_id", relaySessionID).Debug("relay.live: auth failed")
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "invalid relay token", tc.TraceID)
		return
	}

	// Confirm the protocol in the URL matches what was negotiated at session creation.
	if string(sess.Protocol) != protocol {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "protocol mismatch", tc.TraceID)
		return
	}

	streamKey := sess.StreamKey() // "cam_id/profile_id"

	logrus.WithFields(tracectx.FieldsFromEnvelope(tc)).WithFields(logrus.Fields{
		"relay_session_id": relaySessionID,
		"stream_key":       streamKey,
		"protocol":         protocol,
	}).Info("relay.live.serve")

	switch relay.Protocol(protocol) {
	case relay.ProtocolHLS:
		s.serveRelayHLS(w, r, streamKey, tc.TraceID)
	case relay.ProtocolWebRTC:
		s.serveRelayWebRTC(w, streamKey, tc.TraceID)
	default:
		writeJSONError(w, http.StatusBadRequest, "bad_request", "unsupported protocol: "+protocol, tc.TraceID)
	}
}

// serveRelayHLS serves the HLS playlist file directly from disk.
// The HLSSubscriber writes segments to cfg.HLS.RootPath/{streamKey}/ so we can
// read the file here without going through the /hls/{key} route (which cannot
// handle stream keys that contain a slash).
func (s *Server) serveRelayHLS(w http.ResponseWriter, r *http.Request, streamKey, traceID string) {
	playlistPath := filepath.Join(s.cfg.HLS.RootPath, streamKey, "index.m3u8")

	if _, err := os.Stat(playlistPath); os.IsNotExist(err) {
		writeJSONError(w, http.StatusServiceUnavailable, "stream_unavailable",
			"HLS playlist not yet available, retry shortly", traceID)
		w.Header().Set("Retry-After", "2")
		return
	}

	w.Header().Set("Content-Type", "application/x-mpegURL")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, playlistPath)
}

// serveRelayWebRTC returns the Pion offer URL so the client can POST its SDP offer.
// WebRTC requires a client-initiated offer/answer exchange, so we cannot proxy it
// as a simple GET — instead we hand the client the URL it needs to call.
func (s *Server) serveRelayWebRTC(w http.ResponseWriter, streamKey, traceID string) {
	// url.PathEscape encodes '/' as '%2F', keeping it a single URL segment.
	offerPath := "/pion/webrtc/" + url.PathEscape(streamKey) + "/offer"
	writeJSON(w, http.StatusOK, map[string]string{
		"protocol":  "webrtc",
		"offer_url": offerPath,
		"method":    "POST",
		"trace_id":  traceID,
	})
}
