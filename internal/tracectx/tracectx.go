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

// Context carries the minimum trace metadata needed to correlate logs and
// later bridge into a real OpenTelemetry exporter.
type Context struct {
	Traceparent string
	TraceID     string
	SpanID      string
	ParentSpan  string
}

// ExtractOrNew loads a valid W3C traceparent from the request or creates one.
func ExtractOrNew(r *http.Request) Context {
	for _, raw := range []string{
		strings.TrimSpace(r.Header.Get("traceparent")),
		strings.TrimSpace(r.URL.Query().Get("traceparent")),
	} {
		if tc, ok := Parse(raw); ok {
			return Child(tc)
		}
	}
	return New()
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
		return New()
	}

	spanID := make([]byte, 8)
	fillRandom(spanID)
	spanIDHex := hex.EncodeToString(spanID)
	return Context{
		Traceparent: "00-" + parent.TraceID + "-" + spanIDHex + "-01",
		TraceID:     parent.TraceID,
		SpanID:      spanIDHex,
		ParentSpan:  parent.SpanID,
	}
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
	return context.WithValue(ctx, traceContextKey, tc)
}

func FromContext(ctx context.Context) (Context, bool) {
	tc, ok := ctx.Value(traceContextKey).(Context)
	return tc, ok
}

func Fields(ctx context.Context) logrus.Fields {
	tc, ok := FromContext(ctx)
	if !ok {
		return logrus.Fields{}
	}
	return FieldsFromTrace(tc)
}

func FieldsFromTrace(tc Context) logrus.Fields {
	if tc.Traceparent == "" || tc.TraceID == "" || tc.SpanID == "" {
		return logrus.Fields{}
	}
	fields := logrus.Fields{
		"trace_id":    tc.TraceID,
		"traceparent": tc.Traceparent,
		"span_id":     tc.SpanID,
	}
	if tc.ParentSpan != "" {
		fields["parent_span_id"] = tc.ParentSpan
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
