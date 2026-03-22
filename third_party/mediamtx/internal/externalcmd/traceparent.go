package externalcmd

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"time"
)

// NewTraceparent generates a W3C traceparent value for hook propagation.
func NewTraceparent() string {
	traceID := make([]byte, 16)
	spanID := make([]byte, 8)

	fillRandom(traceID)
	fillRandom(spanID)

	return "00-" + hex.EncodeToString(traceID) + "-" + hex.EncodeToString(spanID) + "-01"
}

func fillRandom(buf []byte) {
	if _, err := rand.Read(buf); err == nil && !allZero(buf) {
		return
	}

	now := uint64(time.Now().UnixNano())
	for i := range buf {
		shift := uint((i % 8) * 8)
		buf[i] = byte(now >> shift)
	}
	if len(buf) >= 8 {
		binary.BigEndian.PutUint64(buf[len(buf)-8:], now)
	}
	if allZero(buf) {
		buf[len(buf)-1] = 1
	}
}

func allZero(buf []byte) bool {
	for _, b := range buf {
		if b != 0 {
			return false
		}
	}
	return true
}
