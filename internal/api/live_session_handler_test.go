package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"go-cam-server/internal/relay"
	"go-cam-server/internal/stream"
	"go-cam-server/internal/tracectx"
)

// fakePublisher satisfies stream.Publisher for tests.
type fakePublisher struct {
	key string
}

func (p *fakePublisher) StreamKey() string         { return p.key }
func (p *fakePublisher) NodeID() string            { return "test-node" }
func (p *fakePublisher) StartedAt() time.Time      { return time.Now() }
func (p *fakePublisher) Stats() stream.PublisherStats { return stream.PublisherStats{} }
func (p *fakePublisher) Stop()                     {}

// fakeIngest satisfies stream.IngestStarter.
// On Start it immediately registers a fake publisher so EnsureStream can find it.
type fakeIngest struct {
	mgr *stream.StreamManager
	err error // if set, Start returns this error
}

func (f *fakeIngest) Start(_ context.Context, key string) error {
	if f.err != nil {
		return f.err
	}
	f.mgr.Register(&fakePublisher{key: key})
	return nil
}

func (f *fakeIngest) Stop(key string) {
	f.mgr.Unregister(key)
}

// buildTestServer constructs a minimal *Server for handler tests.
// If fi is non-nil its manager field is wired to the same StreamManager the
// engine uses, so Register calls in fakeIngest.Start are visible to EnsureStream.
func buildTestServer(t *testing.T, fi *fakeIngest) *Server {
	t.Helper()
	mgr := stream.NewStreamManager()
	var ingest stream.IngestStarter
	if fi != nil {
		fi.mgr = mgr // share the same instance
		ingest = fi
	}
	engine := stream.NewEngine(mgr, ingest)
	relayTokenSvc := relay.NewTokenService("test-relay-secret")
	relayMgr := relay.NewManager(relayTokenSvc, "http://relay.test")
	return &Server{
		streamEngine: engine,
		relayManager: relayMgr,
		router:       chi.NewRouter(),
	}
}

func postLiveSession(t *testing.T, s *Server, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/live-sessions", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	// inject empty trace context so handler doesn't crash
	ctx := tracectx.WithContext(req.Context(), tracectx.Context{TraceID: "test-trace"})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	s.createLiveSessionV1(rr, req)
	return rr
}

func TestCreateLiveSession_MissingFields(t *testing.T) {
	s := buildTestServer(t, nil) // nil ingest — not reached for bad request
	rr := postLiveSession(t, s, map[string]any{"cam_id": ""})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCreateLiveSession_NoIngest(t *testing.T) {
	s := buildTestServer(t, nil) // engine with nil ingest
	rr := postLiveSession(t, s, map[string]any{
		"cam_id":     "cam1",
		"profile_id": "hd",
	})
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["code"] != "stream_unavailable" {
		t.Errorf("expected code=stream_unavailable, got %s", resp["code"])
	}
}

func TestCreateLiveSession_IngestError(t *testing.T) {
	s := buildTestServer(t, &fakeIngest{err: context.DeadlineExceeded})
	rr := postLiveSession(t, s, map[string]any{
		"cam_id":     "cam1",
		"profile_id": "hd",
	})
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCreateLiveSession_Happy_WebRTC(t *testing.T) {
	s := buildTestServer(t, &fakeIngest{})
	rr := postLiveSession(t, s, map[string]any{
		"cam_id":     "cam1",
		"profile_id": "hd",
		"client_capabilities": map[string]any{
			"delivery_protocols": []string{"webrtc", "hls"},
		},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp createLiveSessionResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ViewerSessionID == "" {
		t.Error("expected non-empty viewer_session_id")
	}
	if resp.Token == "" {
		t.Error("expected non-empty token")
	}
	if resp.Protocol != relay.ProtocolWebRTC {
		t.Errorf("expected webrtc protocol, got %s", resp.Protocol)
	}
}

func TestCreateLiveSession_Happy_HLSOnly(t *testing.T) {
	s := buildTestServer(t, &fakeIngest{})
	rr := postLiveSession(t, s, map[string]any{
		"cam_id":     "cam2",
		"profile_id": "sd",
		"client_capabilities": map[string]any{
			"delivery_protocols": []string{"hls"},
		},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp createLiveSessionResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Protocol != relay.ProtocolHLS {
		t.Errorf("expected hls protocol, got %s", resp.Protocol)
	}
}

func TestCreateLiveSession_Idempotent(t *testing.T) {
	s := buildTestServer(t, &fakeIngest{})
	body := map[string]any{"cam_id": "cam1", "profile_id": "hd"}
	rr1 := postLiveSession(t, s, body)
	rr2 := postLiveSession(t, s, body)

	if rr1.Code != http.StatusCreated || rr2.Code != http.StatusCreated {
		t.Errorf("both requests should succeed: %d, %d", rr1.Code, rr2.Code)
	}
}

func TestDeleteLiveSession(t *testing.T) {
	s := buildTestServer(t, &fakeIngest{})

	// create a session first
	rr := postLiveSession(t, s, map[string]any{"cam_id": "cam1", "profile_id": "hd"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d", rr.Code)
	}
	var created createLiveSessionResponse
	json.NewDecoder(rr.Body).Decode(&created)

	// delete it
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/live-sessions/"+created.ViewerSessionID, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("viewer_session_id", created.ViewerSessionID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	drr := httptest.NewRecorder()
	s.deleteLiveSessionV1(drr, req)

	if drr.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", drr.Code)
	}
}
