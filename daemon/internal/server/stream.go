package server

import (
	"github.com/sirin-interact/worky/daemon/internal/protocol"
	"github.com/sirin-interact/worky/daemon/internal/session"
	"github.com/sirin-interact/worky/daemon/internal/termlog"
)

// How much history a client gets when it fell so far behind that the output
// it had not received yet is gone.
const catchUpBacklog = 1 << 20

// channel is one attachment of a connection to a session's terminal.
type channel struct {
	id   uint32
	sess *session.Session
	log  *termlog.Log

	// active is set once the client has been told the channel's id. Only
	// then may frames be sent on it.
	active bool

	cursor termlog.Cursor
	// replyFrom is where the output this client may answer begins. Output
	// before it was sent as backlog, or came before the client took the size
	// and the daemon answered it. No output frame crosses it.
	replyFrom uint64
	// pending holds frames to send before reading the log again.
	pending [][]byte
}

func newChannel(id uint32, sess *session.Session, log *termlog.Log, from, backlog *uint64) (*channel, uint64) {
	ch := &channel{id: id, sess: sess, log: log}
	head := ch.open(from, backlog)
	return ch, head
}

// open points the channel at a place in the log and queues the frames a reset
// needs. It returns the end of the log.
func (ch *channel) open(from, backlog *uint64) uint64 {
	start := ch.log.Open(from, backlog)
	ch.cursor = start.Cursor
	ch.replyFrom = max(ch.replyFrom, start.Head)
	ch.pending = ch.pending[:0]
	if !start.Reset {
		return start.Head
	}

	ch.pending = append(ch.pending, protocol.AppendFrame(nil, protocol.Frame{
		Type:    protocol.FrameReset,
		Channel: ch.id,
		Offset:  start.Cursor.Offset,
		Cols:    start.Cols,
		Rows:    start.Rows,
	}))
	for state := start.State; len(state) > 0; {
		n := min(len(state), protocol.MaxFramePayload)
		ch.pending = append(ch.pending, protocol.AppendFrame(nil, protocol.Frame{
			Type:    protocol.FrameState,
			Channel: ch.id,
			Payload: state[:n],
		}))
		state = state[n:]
	}
	return start.Head
}

// next returns the channel's next frame, or false when the client has caught
// up. scratch is reused for the output read from the log.
func (ch *channel) next(scratch []byte) ([]byte, bool) {
	for {
		if len(ch.pending) > 0 {
			frame := ch.pending[0]
			ch.pending = ch.pending[1:]
			return frame, true
		}

		// A frame ends at replyFrom rather than crossing it, so the offset
		// in a reply says which side the reply answers.
		limit := uint64(protocol.MaxFramePayload)
		if ch.cursor.Offset < ch.replyFrom {
			limit = min(limit, ch.replyFrom-ch.cursor.Offset)
		}
		item, ok := ch.log.Next(&ch.cursor, scratch, int(limit))
		if !ok {
			return nil, false
		}
		switch item.Kind {
		case termlog.Output:
			return protocol.AppendFrame(nil, protocol.Frame{
				Type:    protocol.FrameOutput,
				Channel: ch.id,
				Offset:  item.Offset,
				Payload: item.Data,
			}), true
		case termlog.Resize:
			return protocol.AppendFrame(nil, protocol.Frame{
				Type:    protocol.FrameResize,
				Channel: ch.id,
				Offset:  item.Offset,
				Cols:    item.Cols,
				Rows:    item.Rows,
			}), true
		case termlog.Truncated:
			// The client read too slowly. Restart its stream near the end
			// rather than making it attach again.
			backlog := uint64(catchUpBacklog)
			ch.open(nil, &backlog)
		}
	}
}

// acceptsReply reports whether a reply produced after processing output up to
// offset answers output that is this client's to answer.
func (ch *channel) acceptsReply(offset uint64) bool {
	return offset > ch.replyFrom
}
