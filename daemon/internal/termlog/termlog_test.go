package termlog

import (
	"fmt"
	"strings"
	"testing"
)

func u64(v uint64) *uint64 { return &v }

// drain reads a replay to its end and renders it as text: output as is, a
// resize as <colsxrows@offset>.
func drain(t *testing.T, l *Log, c *Cursor, max int) string {
	t.Helper()
	var sb strings.Builder
	for {
		item, ok := l.Next(c, nil, max)
		if !ok {
			return sb.String()
		}
		switch item.Kind {
		case Output:
			sb.Write(item.Data)
		case Resize:
			fmt.Fprintf(&sb, "<%dx%d@%d>", item.Cols, item.Rows, item.Offset)
		case Truncated:
			t.Fatalf("truncated at offset %d after %q", c.Offset, sb.String())
		}
	}
}

func TestReplayFromTheStart(t *testing.T) {
	l := New(Options{Capacity: 64, Cols: 80, Rows: 24})
	l.Write([]byte("hello "))
	l.Write([]byte("world"))

	s := l.Open(nil, nil)
	if !s.Reset || s.Cursor.Offset != 0 || s.Head != 11 || s.Cols != 80 || s.Rows != 24 || len(s.State) != 0 {
		t.Fatalf("start = %+v", s)
	}
	if got := drain(t, l, &s.Cursor, 4); got != "hello world" {
		t.Fatalf("replay = %q", got)
	}
	if s.Cursor.Offset != 11 {
		t.Fatalf("cursor offset = %d, want 11", s.Cursor.Offset)
	}

	l.Write([]byte("!"))
	if got := drain(t, l, &s.Cursor, 4); got != "!" {
		t.Fatalf("live = %q", got)
	}
}

func TestResizeArrivesAtItsOffset(t *testing.T) {
	l := New(Options{Capacity: 64, Cols: 80, Rows: 24})
	l.Write([]byte("abc"))
	l.Resize(100, 30)
	l.Write([]byte("def"))

	s := l.Open(nil, nil)
	if got := drain(t, l, &s.Cursor, 64); got != "abc<100x30@3>def" {
		t.Fatalf("replay = %q", got)
	}
}

func TestResizesWithoutOutputCollapse(t *testing.T) {
	l := New(Options{Capacity: 64, Cols: 80, Rows: 24})
	l.Write([]byte("abc"))
	s := l.Open(nil, nil)
	drain(t, l, &s.Cursor, 64)

	l.Resize(90, 24)
	if got := drain(t, l, &s.Cursor, 64); got != "<90x24@3>" {
		t.Fatalf("first resize = %q", got)
	}
	l.Resize(91, 24)
	l.Resize(92, 24)
	if got := drain(t, l, &s.Cursor, 64); got != "<92x24@3>" {
		t.Fatalf("collapsed resizes = %q", got)
	}
	l.Resize(92, 24) // no change
	if got := drain(t, l, &s.Cursor, 64); got != "" {
		t.Fatalf("unchanged size produced %q", got)
	}

	fresh := l.Open(nil, nil)
	if got := drain(t, l, &fresh.Cursor, 64); got != "abc<92x24@3>" {
		t.Fatalf("fresh replay = %q", got)
	}
}

func TestResume(t *testing.T) {
	l := New(Options{Capacity: 64, Cols: 80, Rows: 24})
	l.Write([]byte("abc"))
	l.Resize(100, 30)
	l.Write([]byte("def"))

	s := l.Open(u64(4), nil)
	if s.Reset || s.Cursor.Offset != 4 || s.Head != 6 {
		t.Fatalf("start = %+v", s)
	}
	if got := drain(t, l, &s.Cursor, 64); got != "ef" {
		t.Fatalf("resume past the resize = %q", got)
	}

	// A resize at exactly the resume offset is sent again.
	s = l.Open(u64(3), nil)
	if got := drain(t, l, &s.Cursor, 64); got != "<100x30@3>def" {
		t.Fatalf("resume at the resize = %q", got)
	}

	// Resuming at the end is valid and yields nothing until more arrives.
	s = l.Open(u64(6), nil)
	if s.Reset {
		t.Fatalf("resume at head was reset")
	}
	if got := drain(t, l, &s.Cursor, 64); got != "" {
		t.Fatalf("resume at head = %q", got)
	}

	// An offset the log never reached cannot be resumed.
	if s = l.Open(u64(7), nil); !s.Reset {
		t.Fatalf("resume past head was not reset")
	}
}

func TestOldOutputIsDropped(t *testing.T) {
	l := New(Options{Capacity: 8, CheckpointEvery: 4, Cols: 80, Rows: 24})
	old := l.Open(nil, nil)

	l.Write([]byte("0123"))
	l.Resize(100, 30)
	l.Write([]byte("4567"))
	l.Write([]byte("89ab")) // pushes "0123" out

	if item, ok := l.Next(&old.Cursor, nil, 64); !ok || item.Kind != Truncated {
		t.Fatalf("stale cursor got %+v, %v", item, ok)
	}

	// The resize happened before the kept range, so it shows up as the size
	// the replay starts with.
	s := l.Open(nil, nil)
	if !s.Reset || s.Cursor.Offset != 4 || s.Cols != 100 || s.Rows != 30 {
		t.Fatalf("start = %+v", s)
	}
	if got := drain(t, l, &s.Cursor, 64); got != "456789ab" {
		t.Fatalf("replay = %q", got)
	}

	if s := l.Open(u64(2), nil); !s.Reset {
		t.Fatalf("resume before the kept range was not reset")
	}
}

func TestWriteLargerThanCapacity(t *testing.T) {
	l := New(Options{Capacity: 4, CheckpointEvery: 1, Cols: 80, Rows: 24})
	l.Write([]byte("abcdefghij"))

	if got := l.Head(); got != 10 {
		t.Fatalf("head = %d, want 10", got)
	}
	s := l.Open(nil, nil)
	if s.Cursor.Offset != 10 {
		t.Fatalf("start offset = %d, want the only checkpoint left, 10", s.Cursor.Offset)
	}

	s = l.Open(u64(6), nil)
	if got := drain(t, l, &s.Cursor, 64); got != "ghij" {
		t.Fatalf("kept tail = %q", got)
	}
}

func TestBacklogLimitsTheReplay(t *testing.T) {
	l := New(Options{Capacity: 64, CheckpointEvery: 4, Cols: 80, Rows: 24})
	for _, chunk := range []string{"aaaa", "bbbb", "cccc", "dd"} {
		l.Write([]byte(chunk))
	}
	// Checkpoints sit at 0, 4, 8 and 12. The head is 14.

	tests := []struct {
		backlog uint64
		want    string
	}{
		{100, "aaaabbbbccccdd"},
		{14, "aaaabbbbccccdd"},
		{10, "bbbbccccdd"},
		{7, "ccccdd"},
		{2, "dd"},
		{1, ""},
		{0, ""},
	}
	for _, tt := range tests {
		s := l.Open(nil, u64(tt.backlog))
		if !s.Reset {
			t.Fatalf("backlog %d: not a reset", tt.backlog)
		}
		if got := drain(t, l, &s.Cursor, 64); got != tt.want {
			t.Errorf("backlog %d: replay = %q, want %q", tt.backlog, got, tt.want)
		}
	}
}

// A replay never starts in the middle of an escape sequence, even when that
// means more history than was asked for.
func TestBacklogDoesNotStartInsideASequence(t *testing.T) {
	l := New(Options{Capacity: 64, CheckpointEvery: 4, Cols: 80, Rows: 24})
	l.Write([]byte("aaaa"))    // checkpoint at 4
	l.Write([]byte("b\x1b[3")) // the head, 8, is inside a sequence

	s := l.Open(nil, u64(1))
	if !s.Reset || s.Cursor.Offset != 4 {
		t.Fatalf("start = %+v, want a reset at 4", s)
	}
	if got := drain(t, l, &s.Cursor, 64); got != "b\x1b[3" {
		t.Errorf("replay = %q", got)
	}

	l.Write([]byte("1m")) // the sequence is complete, so the end is safe again
	s = l.Open(nil, u64(0))
	if s.Cursor.Offset != l.Head() {
		t.Errorf("start = %d, want the end, %d", s.Cursor.Offset, l.Head())
	}
}

func TestResetCarriesTheModesAtItsOffset(t *testing.T) {
	l := New(Options{Capacity: 64, CheckpointEvery: 8, Cols: 80, Rows: 24})
	l.Write([]byte("\x1b[?2004h")) // checkpoint at 8, bracketed paste on
	l.Write([]byte("\x1b[?1000h")) // checkpoint at 16, mouse on as well
	l.Write([]byte("tail"))

	s := l.Open(nil, u64(11))
	if s.Cursor.Offset != 16 || string(s.State) != "\x1b[?1000h\x1b[?2004h" {
		t.Fatalf("start = offset %d, state %q", s.Cursor.Offset, s.State)
	}

	s = l.Open(nil, u64(12))
	if s.Cursor.Offset != 8 || string(s.State) != "\x1b[?2004h" {
		t.Fatalf("start = offset %d, state %q", s.Cursor.Offset, s.State)
	}

	s = l.Open(nil, u64(20))
	if s.Cursor.Offset != 0 || len(s.State) != 0 {
		t.Fatalf("start = offset %d, state %q", s.Cursor.Offset, s.State)
	}

	// Past the last checkpoint the replay starts at the end, with the modes
	// in effect there.
	s = l.Open(nil, u64(0))
	if s.Cursor.Offset != 20 || string(s.State) != "\x1b[?1000h\x1b[?2004h" {
		t.Fatalf("start = offset %d, state %q", s.Cursor.Offset, s.State)
	}
}

func TestNoCheckpointInsideASequence(t *testing.T) {
	l := New(Options{Capacity: 64, CheckpointEvery: 2, Cols: 80, Rows: 24})
	l.Write([]byte("ab\x1b[3")) // ends inside a control sequence
	l.Write([]byte("1m"))       // ends between sequences

	s := l.Open(nil, u64(5))
	if s.Cursor.Offset != 7 {
		t.Fatalf("start offset = %d, want 7, the first safe point", s.Cursor.Offset)
	}
}

func TestWriteReportsDeviceAttributesQueries(t *testing.T) {
	l := New(Options{Capacity: 64, Cols: 80, Rows: 24})
	if got := l.Write([]byte("\x1b[c")); got != 1 {
		t.Fatalf("queries = %d, want 1", got)
	}
	if got := l.Write([]byte("plain")); got != 0 {
		t.Fatalf("queries = %d, want 0", got)
	}
}

func TestWatch(t *testing.T) {
	l := New(Options{Capacity: 64, Cols: 80, Rows: 24})
	ch := make(chan struct{}, 1)
	l.Watch(ch)

	l.Write([]byte("a"))
	l.Write([]byte("b")) // must not block on the full channel
	select {
	case <-ch:
	default:
		t.Fatalf("no wake after a write")
	}

	l.Resize(90, 24)
	select {
	case <-ch:
	default:
		t.Fatalf("no wake after a resize")
	}

	l.Unwatch(ch)
	l.Write([]byte("c"))
	select {
	case <-ch:
		t.Fatalf("wake after Unwatch")
	default:
	}
}
