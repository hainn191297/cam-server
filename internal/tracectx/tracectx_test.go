package tracectx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func TestEnvelopeJSONCarriesContractIDs(t *testing.T) {
	envelope := Envelope{
		TraceID:         "4bf92f3577b34da6a3ce929d0e0e4736",
		RequestID:       "request-1",
		CamID:           "cam-1",
		StreamID:        "stream-1",
		ViewerSessionID: "viewer-1",
		NodeID:          "node-01",
		Traceparent:     testTraceparent,
		SpanID:          "00f067aa0ba902b7",
		ParentSpan:      "abcdef0123456789",
	}

	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	want := map[string]string{
		"trace_id":          envelope.TraceID,
		"request_id":        envelope.RequestID,
		"cam_id":            envelope.CamID,
		"stream_id":         envelope.StreamID,
		"viewer_session_id": envelope.ViewerSessionID,
		"node_id":           envelope.NodeID,
	}
	if len(got) != len(want) {
		t.Fatalf("json fields = %v, want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("json field %q = %q, want %q", key, got[key], value)
		}
	}
}

func TestChildPreservesEnvelopeIDs(t *testing.T) {
	parent, ok := Parse(testTraceparent)
	if !ok {
		t.Fatal("expected test traceparent to parse")
	}
	parent.RequestID = "request-1"
	parent.CamID = "cam-1"
	parent.StreamID = "stream-1"
	parent.ViewerSessionID = "viewer-1"
	parent.NodeID = "node-01"

	child := Child(parent)
	if child.TraceID != parent.TraceID {
		t.Errorf("TraceID = %q, want %q", child.TraceID, parent.TraceID)
	}
	if child.SpanID == "" || child.SpanID == parent.SpanID {
		t.Errorf("SpanID = %q, want a new non-empty span", child.SpanID)
	}
	if child.ParentSpan != parent.SpanID {
		t.Errorf("ParentSpan = %q, want %q", child.ParentSpan, parent.SpanID)
	}
	if child.RequestID != parent.RequestID ||
		child.CamID != parent.CamID ||
		child.StreamID != parent.StreamID ||
		child.ViewerSessionID != parent.ViewerSessionID ||
		child.NodeID != parent.NodeID {
		t.Errorf("child envelope IDs = %+v, want IDs from %+v", child, parent)
	}
}

func TestChildWithMissingTraceStartsTraceAndPreservesEnvelopeIDs(t *testing.T) {
	parent := Envelope{
		RequestID:       "request-1",
		CamID:           "cam-1",
		StreamID:        "stream-1",
		ViewerSessionID: "viewer-1",
		NodeID:          "node-01",
	}

	child := Child(parent)
	if child.TraceID == "" || child.Traceparent == "" || child.SpanID == "" {
		t.Errorf("child trace = %+v, want a new W3C trace", child)
	}
	if child.RequestID != parent.RequestID ||
		child.CamID != parent.CamID ||
		child.StreamID != parent.StreamID ||
		child.ViewerSessionID != parent.ViewerSessionID ||
		child.NodeID != parent.NodeID {
		t.Errorf("child envelope IDs = %+v, want IDs from %+v", child, parent)
	}
}

func TestContextHelpersAndFields(t *testing.T) {
	envelope, ok := Parse(testTraceparent)
	if !ok {
		t.Fatal("expected test traceparent to parse")
	}
	envelope.RequestID = "request-1"
	envelope.CamID = "cam-1"
	envelope.StreamID = "stream-1"
	envelope.ViewerSessionID = "viewer-1"
	envelope.NodeID = "node-01"

	ctx := WithEnvelope(context.Background(), envelope)
	got, ok := EnvelopeFromContext(ctx)
	if !ok {
		t.Fatal("expected envelope in context")
	}
	if got != envelope {
		t.Errorf("envelope = %+v, want %+v", got, envelope)
	}

	fields := Fields(ctx)
	for key, want := range map[string]any{
		"trace_id":          envelope.TraceID,
		"traceparent":       envelope.Traceparent,
		"span_id":           envelope.SpanID,
		"request_id":        envelope.RequestID,
		"cam_id":            envelope.CamID,
		"stream_id":         envelope.StreamID,
		"viewer_session_id": envelope.ViewerSessionID,
		"node_id":           envelope.NodeID,
	} {
		if fields[key] != want {
			t.Errorf("field %q = %#v, want %#v", key, fields[key], want)
		}
	}
}

func TestExtractOrNewKeepsW3CBehaviorAndRequestID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?traceparent="+testTraceparent, nil)
	req.Header.Set("x-request-id", " request-1 ")

	got := ExtractOrNew(req)
	if got.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("TraceID = %q, want incoming trace ID", got.TraceID)
	}
	if got.ParentSpan != "00f067aa0ba902b7" {
		t.Errorf("ParentSpan = %q, want incoming span ID", got.ParentSpan)
	}
	if got.SpanID == "" || got.SpanID == got.ParentSpan {
		t.Errorf("SpanID = %q, want generated child span", got.SpanID)
	}
	if got.RequestID != "request-1" {
		t.Errorf("RequestID = %q, want %q", got.RequestID, "request-1")
	}
}

func TestFieldsFromEnvelopeIncludesBusinessIDsWithoutTrace(t *testing.T) {
	fields := FieldsFromEnvelope(Envelope{
		RequestID: "request-1",
		NodeID:    "node-01",
	})

	if len(fields) != 2 {
		t.Fatalf("fields = %v, want only business IDs", fields)
	}
	if fields["request_id"] != "request-1" || fields["node_id"] != "node-01" {
		t.Errorf("fields = %v, want request_id and node_id", fields)
	}
}
