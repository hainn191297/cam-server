package flv

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"go-cam-server/internal/stream"
)

// Reader reads AVPackets from an FLV byte stream (file or HTTP chunked response).
type Reader struct {
	r io.Reader
}

// NewReader creates a Reader and consumes the 13-byte FLV file header.
// Returns an error if the magic bytes are wrong or the header cannot be read.
func NewReader(r io.Reader) (*Reader, error) {
	var hdr [13]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("flv: read header: %w", err)
	}
	if hdr[0] != 'F' || hdr[1] != 'L' || hdr[2] != 'V' {
		return nil, errors.New("flv: invalid magic bytes")
	}
	return &Reader{r: r}, nil
}

// Next reads and returns the next AVPacket from the stream.
// Returns io.EOF when the stream ends cleanly.
func (rd *Reader) Next() (*stream.AVPacket, error) {
	// Read 11-byte tag header.
	var hdr [11]byte
	if _, err := io.ReadFull(rd.r, hdr[:]); err != nil {
		return nil, err // propagates io.EOF
	}

	tagType := stream.PacketType(hdr[0])

	// DataSize — 24-bit big-endian
	dataLen := int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3])

	// Timestamp — lower 24 bits + extended upper 8 bits
	ts := uint32(hdr[4])<<16 | uint32(hdr[5])<<8 | uint32(hdr[6]) | uint32(hdr[7])<<24

	// StreamID (bytes 8-10) — always 0, ignored.

	// Read tag payload.
	data := make([]byte, dataLen)
	if _, err := io.ReadFull(rd.r, data); err != nil {
		return nil, fmt.Errorf("flv: read tag data: %w", err)
	}

	// Read and discard PreviousTagSize (4 bytes).
	var prevSize [4]byte
	if _, err := io.ReadFull(rd.r, prevSize[:]); err != nil {
		return nil, fmt.Errorf("flv: read prevTagSize: %w", err)
	}

	pkt := &stream.AVPacket{
		Type:      tagType,
		Timestamp: ts,
		Data:      data,
	}

	// Detect keyframe: video tag, first nibble of first byte == 1.
	if tagType == stream.PacketVideo && len(data) > 0 {
		pkt.IsKeyframe = (data[0] >> 4) == 1
	}

	return pkt, nil
}

// ReadPrevTagSize reads the 4-byte PreviousTagSize field and returns its value.
// Exposed for callers that skip it and want to validate (optional).
func readPrevTagSize(r io.Reader) (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(buf[:]), nil
}
