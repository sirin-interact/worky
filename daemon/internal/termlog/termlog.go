// Package termlog keeps a session's terminal output so clients can replay it.
//
// A Log holds the most recent output bytes and the points where the terminal
// size changed. A position in it is an offset: the number of output bytes
// before it, counted from the start of the session.
package termlog

import (
	"sync"

	"github.com/sirin-interact/worky/daemon/internal/vt"
)

// Defaults for Options.
const (
	DefaultCapacity        = 8 << 20
	DefaultCheckpointEvery = 64 << 10
)

// Options configures a Log.
type Options struct {
	// Capacity is how many output bytes the log keeps.
	Capacity int
	// CheckpointEvery is the least output between two points a replay can
	// start from.
	CheckpointEvery int
	// Cols and Rows are the terminal size at the start of the session.
	Cols, Rows uint16
}

// Kind says what an Item is.
type Kind uint8

const (
	// Output is a run of output bytes.
	Output Kind = iota
	// Resize is a change of terminal size.
	Resize
	// Truncated means the cursor points at output the log no longer keeps.
	// The reader has to start over with Open.
	Truncated
)

// Item is one step of a replay.
type Item struct {
	Kind       Kind
	Offset     uint64
	Cols, Rows uint16
	Data       []byte
}

// Cursor is a reader's position in a Log.
type Cursor struct {
	Offset  uint64
	markSeq uint64
}

// Start is where a replay begins.
type Start struct {
	Cursor Cursor
	// Head is the end of the log when the replay was opened.
	Head uint64
	// Reset is true when the reader must discard its terminal first. Cols,
	// Rows and State then describe the terminal at Cursor.Offset.
	Reset      bool
	Cols, Rows uint16
	State      []byte
}

type mark struct {
	seq        uint64
	offset     uint64
	cols, rows uint16
}

type checkpoint struct {
	offset uint64
	state  []byte
}

// Log is safe for concurrent use.
type Log struct {
	mu sync.Mutex

	buf         []byte
	start, head uint64 // the log keeps [start, head)

	baseCols, baseRows uint16 // the size at start, before any kept mark
	cols, rows         uint16 // the size now
	marks              []mark
	nextSeq            uint64

	tracker         vt.Tracker
	checkpoints     []checkpoint
	checkpointEvery uint64
	sinceCheckpoint uint64

	watchers map[chan<- struct{}]struct{}
}

// New returns an empty Log.
func New(o Options) *Log {
	if o.Capacity <= 0 {
		o.Capacity = DefaultCapacity
	}
	if o.CheckpointEvery <= 0 {
		o.CheckpointEvery = DefaultCheckpointEvery
	}
	return &Log{
		buf:             make([]byte, o.Capacity),
		baseCols:        o.Cols,
		baseRows:        o.Rows,
		cols:            o.Cols,
		rows:            o.Rows,
		checkpoints:     []checkpoint{{offset: 0}},
		checkpointEvery: uint64(o.CheckpointEvery),
		watchers:        make(map[chan<- struct{}]struct{}),
	}
}

// Write appends output. It returns how many primary device attributes queries
// the output contained.
func (l *Log) Write(p []byte) (primaryDA int) {
	if len(p) == 0 {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	l.tracker.Write(p)

	// Only the tail of an oversized write can be kept.
	stored := p
	if len(stored) > len(l.buf) {
		l.head += uint64(len(stored) - len(l.buf))
		stored = stored[len(stored)-len(l.buf):]
	}
	at := int(l.head % uint64(len(l.buf)))
	n := copy(l.buf[at:], stored)
	copy(l.buf, stored[n:])
	l.head += uint64(len(stored))

	l.sinceCheckpoint += uint64(len(p))
	if l.sinceCheckpoint >= l.checkpointEvery && l.tracker.Safe() {
		l.checkpoints = append(l.checkpoints, checkpoint{offset: l.head, state: l.tracker.Restore()})
		l.sinceCheckpoint = 0
	}

	if l.head-l.start > uint64(len(l.buf)) {
		l.dropBefore(l.head - uint64(len(l.buf)))
	}

	l.notify()
	return l.tracker.TakePrimaryDA()
}

// dropBefore forgets everything before offset.
func (l *Log) dropBefore(offset uint64) {
	l.start = offset

	keep := 0
	for keep < len(l.marks) && l.marks[keep].offset < offset {
		l.baseCols, l.baseRows = l.marks[keep].cols, l.marks[keep].rows
		keep++
	}
	l.marks = l.marks[keep:]

	keep = 0
	for keep < len(l.checkpoints) && l.checkpoints[keep].offset < offset {
		keep++
	}
	l.checkpoints = l.checkpoints[keep:]
}

// Resize records a change of terminal size at the end of the log.
func (l *Log) Resize(cols, rows uint16) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if cols == l.cols && rows == l.rows {
		return
	}
	l.cols, l.rows = cols, rows

	m := mark{seq: l.nextSeq, offset: l.head, cols: cols, rows: rows}
	l.nextSeq++
	// Several resizes with no output between them collapse into the last.
	if n := len(l.marks); n > 0 && l.marks[n-1].offset == l.head {
		l.marks[n-1] = m
	} else {
		l.marks = append(l.marks, m)
	}
	l.notify()
}

// Head returns the end of the log.
func (l *Log) Head() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.head
}

// sizeAt returns the terminal size in effect at offset.
func (l *Log) sizeAt(offset uint64) (cols, rows uint16) {
	cols, rows = l.baseCols, l.baseRows
	for _, m := range l.marks {
		if m.offset > offset {
			break
		}
		cols, rows = m.cols, m.rows
	}
	return cols, rows
}

// seqFrom returns the seq of the first mark that satisfies keep, or the seq
// the next mark will get.
func (l *Log) seqFrom(keep func(mark) bool) uint64 {
	for _, m := range l.marks {
		if keep(m) {
			return m.seq
		}
	}
	return l.nextSeq
}

// Open starts a replay.
//
// With from, the replay continues at that offset when the log still keeps it.
// Otherwise it starts with a reset: from the oldest point kept, or no more
// than backlog bytes before the end when backlog is given.
func (l *Log) Open(from, backlog *uint64) Start {
	l.mu.Lock()
	defer l.mu.Unlock()

	if from != nil && *from >= l.start && *from <= l.head {
		at := *from
		// A resize at exactly this offset may not have reached the reader, and
		// sending it twice does no harm.
		seq := l.seqFrom(func(m mark) bool { return m.offset >= at })
		return Start{Cursor: Cursor{Offset: at, markSeq: seq}, Head: l.head}
	}

	want := l.start
	if backlog != nil && l.head-l.start > *backlog {
		want = l.head - *backlog
	}

	// The earliest point at or after want that is safe to start from. The end
	// of the log is one when the stream is between sequences.
	cp, found := checkpoint{}, false
	for _, c := range l.checkpoints {
		if c.offset >= want {
			cp, found = c, true
			break
		}
	}
	if !found {
		// Either the end is safe, or nothing better is left to choose.
		cp = checkpoint{offset: l.head, state: l.tracker.Restore()}
	}

	cols, rows := l.sizeAt(cp.offset)
	seq := l.seqFrom(func(m mark) bool { return m.offset > cp.offset })
	return Start{
		Cursor: Cursor{Offset: cp.offset, markSeq: seq},
		Head:   l.head,
		Reset:  true,
		Cols:   cols,
		Rows:   rows,
		State:  cp.state,
	}
}

// Next returns the next step of a replay and advances the cursor. It reports
// false when the reader has caught up. Output data is appended to dst[:0] and
// is at most max bytes long.
func (l *Log) Next(c *Cursor, dst []byte, max int) (Item, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if c.Offset < l.start {
		return Item{Kind: Truncated}, true
	}

	limit := l.head
	for _, m := range l.marks {
		if m.seq < c.markSeq {
			continue
		}
		if m.offset <= c.Offset {
			c.markSeq = m.seq + 1
			return Item{Kind: Resize, Offset: m.offset, Cols: m.cols, Rows: m.rows}, true
		}
		limit = min(limit, m.offset)
		break
	}

	if c.Offset >= limit {
		return Item{}, false
	}
	n := int(min(uint64(max), limit-c.Offset))
	at := int(c.Offset % uint64(len(l.buf)))
	dst = append(dst[:0], l.buf[at:min(at+n, len(l.buf))]...)
	dst = append(dst, l.buf[:n-len(dst)]...)

	item := Item{Kind: Output, Offset: c.Offset, Data: dst}
	c.Offset += uint64(n)
	return item, true
}

// Watch makes the log send on ch, without blocking, whenever it changes.
func (l *Log) Watch(ch chan<- struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.watchers[ch] = struct{}{}
}

// Unwatch undoes Watch.
func (l *Log) Unwatch(ch chan<- struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.watchers, ch)
}

// Wake signals every watcher, as if the log had changed.
func (l *Log) Wake() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.notify()
}

func (l *Log) notify() {
	for ch := range l.watchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
