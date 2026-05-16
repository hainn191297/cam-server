package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"go-cam-server/internal/relay"
	"go-cam-server/internal/stream"
	"go-cam-server/internal/tracectx"
)

type createLiveSessionRequest struct {
	CamID     string `json:"cam_id"`
	ProfileID string `json:"profile_id"`
	ClientCaps struct {
		DeliveryProtocols []relay.Protocol `json:"delivery_protocols"`
	} `json:"client_capabilities"`
}

type createLiveSessionResponse struct {
	ViewerSessionID string         `json:"viewer_session_id"`
	StreamID        string         `json:"stream_id"`
	Protocol        relay.Protocol `json:"protocol"`
	Endpoint        string         `json:"endpoint"`
	Token           string         `json:"token"`
	ExpiresAt       time.Time      `json:"expires_at"`
	Capabilities    struct {
		SupportsAudio bool `json:"supports_audio"`
		Reconnect     bool `json:"reconnect"`
	} `json:"capabilities"`
}

func (s *Server) createLiveSessionV1(w http.ResponseWriter, r *http.Request) {
	tc, _ := tracectx.FromContext(r.Context())

	var req createLiveSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON", tc.TraceID)
		return
	}
	if req.CamID == "" || req.ProfileID == "" {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "cam_id and profile_id required", tc.TraceID)
		return
	}

	// default protocols
	protocols := req.ClientCaps.DeliveryProtocols
	if len(protocols) == 0 {
		protocols = []relay.Protocol{relay.ProtocolWebRTC, relay.ProtocolHLS}
	}

	// enrich trace
	tc.CamID = req.CamID
	ctx := tracectx.WithContext(r.Context(), tc)

	logrus.WithFields(tracectx.FieldsFromEnvelope(tc)).Info("live.session.create")

	// ensure shared stream
	key := stream.StreamKey{CamID: req.CamID, ProfileID: req.ProfileID}
	streamID, _, err := s.streamEngine.EnsureStream(ctx, key)
	if err != nil {
		logrus.WithError(err).WithFields(tracectx.FieldsFromEnvelope(tc)).Error("live.session: stream unavailable")
		writeJSONError(w, http.StatusServiceUnavailable, "stream_unavailable", err.Error(), tc.TraceID)
		return
	}

	// enrich trace with stream_id
	tc.StreamID = streamID
	ctx = tracectx.WithContext(ctx, tc)

	// create relay session
	viewerSessionID := uuid.New().String()
	tc.ViewerSessionID = viewerSessionID
	ctx = tracectx.WithContext(ctx, tc)
	_ = ctx // ctx enriched; used for future downstream calls

	expiresAt := time.Now().Add(8 * time.Hour)
	relayResp, err := s.relayManager.Create(relay.CreateSessionRequest{
		RelaySessionID:     viewerSessionID,
		StreamID:           streamID,
		CamID:              req.CamID,
		ProfileID:          req.ProfileID,
		RequestedProtocols: protocols,
		TokenExpiresAt:     expiresAt,
	})
	if err != nil {
		logrus.WithError(err).WithFields(tracectx.FieldsFromEnvelope(tc)).Error("live.session: relay unavailable")
		writeJSONError(w, http.StatusServiceUnavailable, "relay_unavailable", err.Error(), tc.TraceID)
		return
	}

	logrus.WithFields(tracectx.FieldsFromEnvelope(tc)).WithFields(logrus.Fields{
		"protocol": relayResp.Protocol,
		"endpoint": relayResp.Endpoint,
	}).Info("relay.session.create")

	resp := createLiveSessionResponse{
		ViewerSessionID: viewerSessionID,
		StreamID:        streamID,
		Protocol:        relayResp.Protocol,
		Endpoint:        relayResp.Endpoint,
		Token:           relayResp.Token,
		ExpiresAt:       expiresAt,
	}
	resp.Capabilities.Reconnect = true

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) deleteLiveSessionV1(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "viewer_session_id")
	s.relayManager.Close(id)
	w.WriteHeader(http.StatusNoContent)
}

func writeJSONError(w http.ResponseWriter, status int, code, msg, traceID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"code":     code,
		"message":  msg,
		"trace_id": traceID,
	})
}
