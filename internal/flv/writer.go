// Package flv provides a minimal FLV container writer and reader.
//
// FLV (Flash Video) binary layout:
//
//	File header (13 bytes):
//	  'F' 'L' 'V'              3 bytes  magic
//	  0x01                     1 byte   version
//	  0x05                     1 byte   flags (0x01=audio, 0x04=video, 0x05=both)
//	  0x00 0x00 0x00 0x09      4 bytes  header size (always 9, big-endian)
//	  0x00 0x00 0x00 0x00      4 bytes  PreviousTagSize0 (always 0)
//
//	Tag (11 + N + 4 bytes, repeating):
//	  [0]     TagType         1 byte   0x08=audio, 0x09=video, 0x12=scriptdata
//	  [1-3]   DataSize        3 bytes  len(payload), big-endian
//	  [4-6]   Timestamp       3 bytes  lower 24 bits of ms timestamp, big-endian
//	  [7]     TimestampExt    1 byte   upper 8 bits of ms timestamp
//	  [8-10]  StreamID        3 bytes  always 0
//	  [11..N] Data            N bytes  tag payload (AVPacket.Data)
//	  [N+1..N+4] PrevTagSize  4 bytes  11 + N, big-endian
package flv

import (
	"encoding/binary"
	"io"

	"go-cam-server/internal/stream"
)

// fileHeader is the 13-byte FLV file header written once at the start of every FLV file/stream.
var fileHeader = [13]byte{
	'F', 'L', 'V', // magic
	0x01,                   // version 1
	0x05,                   // flags: audio + video
	0x00, 0x00, 0x00, 0x09, // data offset = 9 (big-endian uint32)
	0x00, 0x00, 0x00, 0x00, // PreviousTagSize0 = 0
}

// WriteFileHeader writes the 13-byte FLV file header to w.
// Must be called exactly once before any WriteTag calls on the same writer.
func WriteFileHeader(w io.Writer) error {
	_, err := w.Write(fileHeader[:])
	return err
}

// WriteTag serialises pkt as a single FLV tag and writes it to w.
// The caller must have already written the file header via WriteFileHeader.
func WriteTag(w io.Writer, pkt *stream.AVPacket) error {
	dataLen := len(pkt.Data)
	ts := pkt.Timestamp

	// Build the 11-byte tag header.
	var hdr [11]byte
	hdr[0] = byte(pkt.Type)
	// DataSize — 24-bit big-endian
	hdr[1] = byte(dataLen >> 16)
	hdr[2] = byte(dataLen >> 8)
	hdr[3] = byte(dataLen)
	// Timestamp — lower 24 bits big-endian, then extended byte
	hdr[4] = byte(ts >> 16)
	hdr[5] = byte(ts >> 8)
	hdr[6] = byte(ts)
	hdr[7] = byte(ts >> 24) // TimestampExtended (upper 8 bits)
	// StreamID — always 0
	hdr[8], hdr[9], hdr[10] = 0, 0, 0

	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := w.Write(pkt.Data); err != nil {
		return err
	}

	// PreviousTagSize = 11 (header) + dataLen
	var prevSize [4]byte
	binary.BigEndian.PutUint32(prevSize[:], uint32(11+dataLen))
	_, err := w.Write(prevSize[:])
	return err
}
