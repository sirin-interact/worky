package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Binary frame types.
const (
	FrameOutput byte = 0x01
	FrameInput  byte = 0x02
)

// MaxFramePayload is the largest payload either side may put in one frame.
const MaxFramePayload = 64 * 1024

const (
	channelEnd   = 5
	outputHeader = 13
)

var (
	// ErrUnknownFrame reports a frame type this version does not know.
	// Receivers ignore such frames.
	ErrUnknownFrame = errors.New("unknown frame type")
	// ErrMalformedFrame reports a frame too short for its type.
	ErrMalformedFrame = errors.New("malformed frame")
)

// Frame is a decoded binary frame. Offset is set only for output frames.
type Frame struct {
	Type    byte
	Channel uint32
	Offset  uint64
	Payload []byte
}

// AppendOutput appends an output frame to dst.
func AppendOutput(dst []byte, channel uint32, offset uint64, payload []byte) []byte {
	dst = append(dst, FrameOutput)
	dst = binary.BigEndian.AppendUint32(dst, channel)
	dst = binary.BigEndian.AppendUint64(dst, offset)
	return append(dst, payload...)
}

// AppendInput appends an input frame to dst.
func AppendInput(dst []byte, channel uint32, payload []byte) []byte {
	dst = append(dst, FrameInput)
	dst = binary.BigEndian.AppendUint32(dst, channel)
	return append(dst, payload...)
}

// ParseFrame decodes one binary frame. The payload aliases b.
func ParseFrame(b []byte) (Frame, error) {
	if len(b) < channelEnd {
		return Frame{}, fmt.Errorf("%w: %d bytes", ErrMalformedFrame, len(b))
	}
	f := Frame{Type: b[0], Channel: binary.BigEndian.Uint32(b[1:channelEnd])}

	switch f.Type {
	case FrameOutput:
		if len(b) < outputHeader {
			return Frame{}, fmt.Errorf("%w: output frame of %d bytes", ErrMalformedFrame, len(b))
		}
		f.Offset = binary.BigEndian.Uint64(b[channelEnd:outputHeader])
		f.Payload = b[outputHeader:]
	case FrameInput:
		f.Payload = b[channelEnd:]
	default:
		return Frame{}, fmt.Errorf("%w: 0x%02x", ErrUnknownFrame, f.Type)
	}
	return f, nil
}
