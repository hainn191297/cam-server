package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"

	"go-cam-server/internal/ha"
)

var errStreamNotFound = errors.New("stream not found")

type createSessionRequest struct {
	TenantID          string `json:"tenant_id"`
	DeviceID          string `json:"device_id"`
	ViewerID          string `json:"viewer_id"`
	PreferredProtocol string `json:"preferred_protocol"`
	LastKnownEndpoint string `json:"last_known_endpoint"`
}

type reattachSessionRequest struct {
	ReattachToken string `json:"reattach_token"`
}

type sessionURLs struct {
	HLS         string `json:"hls"`
	WebRTC      string `json:"webrtc,omitempty"`
	WebRTCOffer string `json:"webrtc_offer_url,omitempty"`
}

type reconnectPolicy struct {
	InitialDelayMs int     `json:"initial_delay_ms"`
	MaxDelayMs     int     `json:"max_delay_ms"`
	Multiplier     float64 `json:"multiplier"`
	MaxAttempts    int     `json:"max_attempts"`
	JitterFraction float64 `json:"jitter_fraction"`
}

type sessionResponse struct {
	SessionID           string          `json:"session_id"`
	StreamKey           string          `json:"stream_key"`
	TenantID            string          `json:"tenant_id"`
	DeviceID            string          `json:"device_id"`
	ViewerID            string          `json:"viewer_id"`
	RouteStatus         string          `json:"route_status"`
	Mode                ha.HealthMode   `json:"mode"`
	ControlPlaneBackend string          `json:"control_plane_backend"`
	IssuedAt            time.Time       `json:"issued_at"`
	LeaseExpiresAt      time.Time       `json:"lease_expires_at"`
	ReattachToken       string          `json:"reattach_token"`
	URLs                sessionURLs     `json:"urls"`
	RetryPolicy         reconnectPolicy `json:"retry_policy"`
	Warning             string          `json:"warning,omitempty"`
}

var defaultReconnectPolicy = reconnectPolicy{
	InitialDelayMs: 200,
	MaxDelayMs:     5000,
	Multiplier:     2,
	MaxAttempts:    6,
	JitterFraction: 0.2,
}

// POST /live/{key}/session
//
// Creates a short-lived session lease for web/app reconnect continuity.
// Supports Idempotency-Key for retry-safe client bootstrapping.
func (s *Server) createLiveSession(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(chi.URLParam(r, "key"))
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "stream key is required"})
		return
	}

	var req createSessionRequest
	if err := decodeOptionalJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
		return
	}
	req = normalizeCreateSessionRequest(req, key)

	if err := s.ensureStreamReady(r.Context(), key); err != nil {
		if errors.Is(err, errStreamNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "stream not found"})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "stream route unavailable: " + err.Error()})
		return
	}

	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	cacheKey := ""
	if idempotencyKey != "" {
		cacheKey = "live-session:" + key + ":" + idempotencyKey
	}
	requestID := middleware.GetReqID(r.Context())

	result, replayed, err := s.idempotency.Do(r.Context(), cacheKey, func(ctx context.Context) (ha.CachedResponse, error) {
		payload, err := s.issueSession(ctx, key, req, requestID, "live")
		if err != nil {
			return ha.CachedResponse{}, err
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return ha.CachedResponse{}, err
		}
		return ha.CachedResponse{
			StatusCode:  http.StatusCreated,
			ContentType: "application/json",
			Body:        raw,
		}, nil
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create session failed: " + err.Error()})
		return
	}

	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	if result.ContentType != "" {
		w.Header().Set("Content-Type", result.ContentType)
	}
	w.WriteHeader(result.StatusCode)
	_, _ = w.Write(result.Body)
}

// POST /live/session/reattach
//
// Validates a reattach token, refreshes lease TTL, and returns updated routing.
func (s *Server) reattachLiveSession(w http.ResponseWriter, r *http.Request) {
	var req reattachSessionRequest
	if err := decodeRequiredJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
		return
	}

	req.ReattachToken = strings.TrimSpace(req.ReattachToken)
	if req.ReattachToken == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "reattach_token is required"})
		return
	}

	lease, renewedToken, err := s.leaseService.Reattach(r.Context(), req.ReattachToken)
	if err != nil {
		switch {
		case errors.Is(err, ha.ErrInvalidToken):
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		case errors.Is(err, ha.ErrLeaseExpired):
			writeJSON(w, http.StatusGone, map[string]string{"error": err.Error()})
		case errors.Is(err, ha.ErrLeaseMissing):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reattach failed: " + err.Error()})
		}
		return
	}

	mode := s.healthMonitor.Snapshot(r.Context()).Mode
	routeStatus := "live"
	warning := ""
	if err := s.ensureStreamReady(r.Context(), lease.StreamKey); err != nil {
		routeStatus = "last_known"
		warning = "stream not currently routable, using last known endpoint"
	}

	resp := s.buildSessionResponse(lease, renewedToken, mode, routeStatus, warning)
	resp.URLs = sessionURLs{
		HLS:         lease.HLSURL,
		WebRTC:      lease.WebRTCURL,
		WebRTCOffer: lease.WebRTCOfferURL,
	}
	if routeStatus == "live" {
		urls := s.resolveLiveURLs(lease.StreamKey)
		resp.URLs = sessionURLs{
			HLS:         urls.HLS,
			WebRTC:      urls.WebRTC,
			WebRTCOffer: urls.WebRTCOffer,
		}
	}

	requestID := middleware.GetReqID(r.Context())
	logrus.WithFields(logrus.Fields{
		"tenant_id":  lease.TenantID,
		"device_id":  lease.DeviceID,
		"stream_id":  lease.StreamKey,
		"region":     s.cfg.Topology.Region,
		"az":         s.cfg.Topology.AZ,
		"request_id": requestID,
		"lease_id":   lease.ID,
		"mode":       mode,
	}).Info("live.session.reattach")

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) issueSession(
	ctx context.Context,
	streamKey string,
	req createSessionRequest,
	requestID string,
	routeStatus string,
) (sessionResponse, error) {
	urls := s.resolveLiveURLs(streamKey)
	health := s.healthMonitor.Snapshot(ctx)

	lease, token, err := s.leaseService.Issue(ctx, ha.LeaseInput{
		StreamKey:         streamKey,
		TenantID:          req.TenantID,
		DeviceID:          req.DeviceID,
		ViewerID:          req.ViewerID,
		PreferredProtocol: req.PreferredProtocol,
		LastKnownEndpoint: req.LastKnownEndpoint,
		HLSURL:            urls.HLS,
		WebRTCURL:         urls.WebRTC,
		WebRTCOfferURL:    urls.WebRTCOffer,
	})
	if err != nil {
		return sessionResponse{}, err
	}

	logrus.WithFields(logrus.Fields{
		"tenant_id":  lease.TenantID,
		"device_id":  lease.DeviceID,
		"stream_id":  lease.StreamKey,
		"region":     s.cfg.Topology.Region,
		"az":         s.cfg.Topology.AZ,
		"request_id": requestID,
		"lease_id":   lease.ID,
		"mode":       health.Mode,
	}).Info("live.session.bootstrap")

	return s.buildSessionResponse(lease, token, health.Mode, routeStatus, ""), nil
}

func (s *Server) buildSessionResponse(
	lease ha.Lease,
	token string,
	mode ha.HealthMode,
	routeStatus string,
	warning string,
) sessionResponse {
	return sessionResponse{
		SessionID:           lease.ID,
		StreamKey:           lease.StreamKey,
		TenantID:            lease.TenantID,
		DeviceID:            lease.DeviceID,
		ViewerID:            lease.ViewerID,
		RouteStatus:         routeStatus,
		Mode:                mode,
		ControlPlaneBackend: lease.ControlPlaneBackend,
		IssuedAt:            lease.IssuedAt,
		LeaseExpiresAt:      lease.ExpiresAt,
		ReattachToken:       token,
		URLs: sessionURLs{
			HLS:         lease.HLSURL,
			WebRTC:      lease.WebRTCURL,
			WebRTCOffer: lease.WebRTCOfferURL,
		},
		RetryPolicy: defaultReconnectPolicy,
		Warning:     warning,
	}
}

func (s *Server) ensureStreamReady(ctx context.Context, streamKey string) error {
	streamKey = strings.Trim(strings.TrimSpace(streamKey), "/")
	if streamKey == "" {
		return errStreamNotFound
	}

	if _, ok := s.manager.Get(streamKey); ok {
		return nil
	}
	if s.mtx == nil {
		return errStreamNotFound
	}

	return ha.Retry(ctx, ha.RetryPolicy{
		Attempts:       3,
		InitialDelay:   120 * time.Millisecond,
		MaxDelay:       900 * time.Millisecond,
		Multiplier:     2,
		JitterFraction: 0.2,
	}, func(ctx context.Context) error {
		paths, err := s.mtx.ListPaths(ctx)
		if err != nil {
			return err
		}
		for _, p := range paths {
			if p.Name == streamKey && p.Ready {
				return nil
			}
		}
		return errStreamNotFound
	})
}

func decodeOptionalJSON(r io.Reader, dst any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

func decodeRequiredJSON(r io.Reader, dst any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func normalizeCreateSessionRequest(req createSessionRequest, streamKey string) createSessionRequest {
	req.TenantID = normalizeLabelValue(req.TenantID, "default")
	req.ViewerID = normalizeLabelValue(req.ViewerID, "anonymous")
	deviceFallback := streamKey
	if req.DeviceID == "" {
		req.DeviceID = deviceFallback
	}
	req.DeviceID = normalizeLabelValue(req.DeviceID, deviceFallback)

	preferred := strings.ToLower(strings.TrimSpace(req.PreferredProtocol))
	switch preferred {
	case "hls", "webrtc":
		req.PreferredProtocol = preferred
	default:
		req.PreferredProtocol = "hls"
	}
	req.LastKnownEndpoint = strings.TrimSpace(req.LastKnownEndpoint)
	return req
}

func normalizeLabelValue(v, fallback string) string {
	cleaned := strings.TrimSpace(v)
	if cleaned == "" {
		return fallback
	}
	return cleaned
}
