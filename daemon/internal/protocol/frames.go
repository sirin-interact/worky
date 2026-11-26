package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Binary frame types. The first four come from the daemon, the rest from a
// client.
const (
	FrameOutput byte = 0x01
	FrameReset  byte = 0x02
	FrameState  byte = 0x03
	FrameResize byte = 0x04
	FrameInput  byte = 0x10
	FrameReply  byte = 0x11
)

// MaxFramePayload is the largest payload either side may put in one frame.
const MaxFramePayload = 64 * 1024

const (
	headerLen = 5             // type + channel
	offsetEnd = headerLen + 8 // + offset
	sizeEnd   = offsetEnd + 4 // + cols + rows
)

var (
	// ErrUnknownFrame reports a frame type this version does not know.
	// Receivers ignore such frames.
	ErrUnknownFrame = errors.New("unknown frame type")
	// ErrMalformedFrame reports a frame too short for its type.
	ErrMalformedFrame = errors.New("malformed frame")
)

// Frame is a decoded binary frame. Which fields are set depends on Type:
// output and reply have Offset and Payload, reset and resize have Offset, Cols
// and Rows, state and input have Payload.
type Frame struct {
	Type    byte
	Channel uint32
	Offset  uint64
	Cols    uint16
	Rows    uint16
	Payload []byte
}

// AppendFrame appends the encoding of f to dst. It panics on a frame type
// this version does not define.
func AppendFrame(dst []byte, f Frame) []byte {
	dst = append(dst, f.Type)
	dst = binary.BigEndian.AppendUint32(dst, f.Channel)

	switch f.Type {
	case FrameOutput, FrameReply:
		dst = binary.BigEndian.AppendUint64(dst, f.Offset)
		return append(dst, f.Payload...)
	case FrameReset, FrameResize:
		dst = binary.BigEndian.AppendUint64(dst, f.Offset)
		dst = binary.BigEndian.AppendUint16(dst, f.Cols)
		return binary.BigEndian.AppendUint16(dst, f.Rows)
	case FrameState, FrameInput:
		return append(dst, f.Payload...)
	default:
		panic(fmt.Sprintf("protocol: cannot encode frame type 0x%02x", f.Type))
	}
}

// ParseFrame decodes one binary frame. The payload aliases b.
func ParseFrame(b []byte) (Frame, error) {
	if len(b) < headerLen {
		return Frame{}, fmt.Errorf("%w: %d bytes", ErrMalformedFrame, len(b))
	}
	f := Frame{Type: b[0], Channel: binary.BigEndian.Uint32(b[1:headerLen])}

	need := func(n int) error {
		if len(b) < n {
			return fmt.Errorf("%w: type 0x%02x with %d bytes", ErrMalformedFrame, f.Type, len(b))
		}
		return nil
	}

	switch f.Type {
	case FrameOutput, FrameReply:
		if err := need(offsetEnd); err != nil {
			return Frame{}, err
		}
		f.Offset = binary.BigEndian.Uint64(b[headerLen:offsetEnd])
		f.Payload = b[offsetEnd:]
	case FrameReset, FrameResize:
		if err := need(sizeEnd); err != nil {
			return Frame{}, err
		}
		f.Offset = binary.BigEndian.Uint64(b[headerLen:offsetEnd])
		f.Cols = binary.BigEndian.Uint16(b[offsetEnd : offsetEnd+2])
		f.Rows = binary.BigEndian.Uint16(b[offsetEnd+2 : sizeEnd])
	case FrameState, FrameInput:
		f.Payload = b[headerLen:]
	default:
		return Frame{}, fmt.Errorf("%w: 0x%02x", ErrUnknownFrame, f.Type)
	}
	return f, nil
}
