package tracectx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

type contextKey string

const traceContextKey contextKey = "trace_context"

// Envelope carries W3C trace state plus the business identifiers used to
// correlate control-plane, media-plane, and viewer activity.
type Envelope struct {
	TraceID         string `json:"trace_id,omitempty"`
	RequestID       string `json:"request_id,omitempty"`
	CamID           string `json:"cam_id,omitempty"`
	StreamID        string `json:"stream_id,omitempty"`
	ViewerSessionID string `json:"viewer_session_id,omitempty"`
	NodeID          string `json:"node_id,omitempty"`

	Traceparent string `json:"-"`
	SpanID      string `json:"-"`
	ParentSpan  string `json:"-"`
}

// Context remains as the old package-facing name for compatibility with the
// existing W3C traceparent callers.
type Context = Envelope

// ExtractOrNew loads a valid W3C traceparent from the request or creates one.
func ExtractOrNew(r *http.Request) Context {
	for _, raw := range []string{
		strings.TrimSpace(r.Header.Get("traceparent")),
		strings.TrimSpace(r.URL.Query().Get("traceparent")),
	} {
		if tc, ok := Parse(raw); ok {
			child := Child(tc)
			child.RequestID = strings.TrimSpace(r.Header.Get("x-request-id"))
			return child
		}
	}
	tc := New()
	tc.RequestID = strings.TrimSpace(r.Header.Get("x-request-id"))
	return tc
}

// New creates a root trace context.
func New() Context {
	traceID := make([]byte, 16)
	spanID := make([]byte, 8)
	fillRandom(traceID)
	fillRandom(spanID)

	traceIDHex := hex.EncodeToString(traceID)
	spanIDHex := hex.EncodeToString(spanID)
	return Context{
		Traceparent: "00-" + traceIDHex + "-" + spanIDHex + "-01",
		TraceID:     traceIDHex,
		SpanID:      spanIDHex,
	}
}

// Child creates a new span within the same trace.
func Child(parent Context) Context {
	if parent.TraceID == "" {
		child := New()
		child.RequestID = parent.RequestID
		child.CamID = parent.CamID
		child.StreamID = parent.StreamID
		child.ViewerSessionID = parent.ViewerSessionID
		child.NodeID = parent.NodeID
		return child
	}

	spanID := make([]byte, 8)
	fillRandom(spanID)
	spanIDHex := hex.EncodeToString(spanID)
	child := parent
	child.Traceparent = "00-" + parent.TraceID + "-" + spanIDHex + "-01"
	child.SpanID = spanIDHex
	child.ParentSpan = parent.SpanID
	return child
}

// Parse validates and decodes a traceparent value.
func Parse(raw string) (Context, bool) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	parts := strings.Split(raw, "-")
	if len(parts) != 4 || len(parts[0]) != 2 || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return Context{}, false
	}
	if !isHex(parts[0]) || !isHex(parts[1]) || !isHex(parts[2]) || !isHex(parts[3]) {
		return Context{}, false
	}
	if allZeroHex(parts[1]) || allZeroHex(parts[2]) {
		return Context{}, false
	}
	return Context{
		Traceparent: raw,
		TraceID:     parts[1],
		SpanID:      parts[2],
	}, true
}

func WithContext(ctx context.Context, tc Context) context.Context {
	return WithEnvelope(ctx, tc)
}

func FromContext(ctx context.Context) (Context, bool) {
	return EnvelopeFromContext(ctx)
}

// WithEnvelope stores a trace envelope on the context.
func WithEnvelope(ctx context.Context, envelope Envelope) context.Context {
	return context.WithValue(ctx, traceContextKey, envelope)
}

// EnvelopeFromContext loads a trace envelope stored by WithEnvelope or the
// backwards-compatible WithContext helper.
func EnvelopeFromContext(ctx context.Context) (Envelope, bool) {
	envelope, ok := ctx.Value(traceContextKey).(Envelope)
	return envelope, ok
}

func Fields(ctx context.Context) logrus.Fields {
	envelope, ok := EnvelopeFromContext(ctx)
	if !ok {
		return logrus.Fields{}
	}
	return FieldsFromEnvelope(envelope)
}

func FieldsFromTrace(tc Context) logrus.Fields {
	return FieldsFromEnvelope(tc)
}

// FieldsFromEnvelope returns structured log fields for every known envelope
// identifier and only emits W3C span data when the trace state is complete.
func FieldsFromEnvelope(envelope Envelope) logrus.Fields {
	fields := logrus.Fields{}
	if envelope.Traceparent != "" && envelope.TraceID != "" && envelope.SpanID != "" {
		fields["trace_id"] = envelope.TraceID
		fields["traceparent"] = envelope.Traceparent
		fields["span_id"] = envelope.SpanID
		if envelope.ParentSpan != "" {
			fields["parent_span_id"] = envelope.ParentSpan
		}
	}
	for name, value := range map[string]string{
		"request_id":        envelope.RequestID,
		"cam_id":            envelope.CamID,
		"stream_id":         envelope.StreamID,
		"viewer_session_id": envelope.ViewerSessionID,
		"node_id":           envelope.NodeID,
	} {
		if value != "" {
			fields[name] = value
		}
	}
	return fields
}

func fillRandom(buf []byte) {
	if _, err := rand.Read(buf); err == nil && !allZeroBytes(buf) {
		return
	}

	fallback := []byte(time.Now().UTC().Format("20060102150405.000000000"))
	for i := range buf {
		buf[i] = fallback[i%len(fallback)]
	}
	if allZeroBytes(buf) {
		buf[len(buf)-1] = 1
	}
}

func allZeroBytes(buf []byte) bool {
	for _, b := range buf {
		if b != 0 {
			return false
		}
	}
	return true
}

func allZeroHex(s string) bool {
	for _, r := range s {
		if r != '0' {
			return false
		}
	}
	return true
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}
