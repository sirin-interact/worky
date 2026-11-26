// Package vt follows a terminal output stream closely enough to restart it
// partway through, without emulating a terminal.
//
// A Tracker answers two questions about a position in the stream: is it between
// escape sequences and characters, so a replay can start there, and which
// terminal modes were in effect, so a replay starting there can restore them.
package vt

import (
	"sort"
	"strconv"
)

type state uint8

const (
	ground    state = iota
	escape          // after ESC
	escapeMid       // after ESC and an intermediate byte
	csi             // after ESC [
	oscString       // after ESC ], ends at BEL or ST
	string_         // DCS, SOS, PM or APC, ends at ST
	stringEsc       // ESC seen inside a string, maybe the start of ST
)

const (
	maxParams    = 16
	maxModes     = 128
	maxKittyDeep = 16
)

// Tracker follows a terminal output stream. The zero value is ready to use.
type Tracker struct {
	st       state
	prev     state // the string state to return to from stringEsc
	utf8Left int   // continuation bytes still expected

	// The control sequence being read.
	prefix  byte
	inter   byte
	interN  int
	params  [maxParams]int
	nparams int

	// What a replay has to restore.
	modes       map[int]bool // DEC private modes, by their last explicit value
	keypadApp   bool
	kitty       []int // keyboard protocol flag stack
	otherKeys   int
	otherKeysOn bool
	cursorStyle int
	cursorSet   bool

	primaryDA int
}

// Write feeds output to the tracker.
func (t *Tracker) Write(p []byte) {
	for _, b := range p {
		t.step(b)
	}
}

// Safe reports whether the stream is between escape sequences and characters.
func (t *Tracker) Safe() bool {
	return t.st == ground && t.utf8Left == 0
}

// TakePrimaryDA returns how many primary device attributes queries were seen
// since the last call.
func (t *Tracker) TakePrimaryDA() int {
	n := t.primaryDA
	t.primaryDA = 0
	return n
}

func (t *Tracker) step(b byte) {
	switch t.st {
	case ground:
		t.stepGround(b)
	case escape:
		t.stepEscape(b)
	case escapeMid:
		switch {
		case b == 0x1b:
			t.st = escape
		case b >= 0x20 && b <= 0x2f:
		default:
			t.st = ground
		}
	case csi:
		t.stepCSI(b)
	case oscString, string_:
		switch {
		case b == 0x1b:
			t.prev, t.st = t.st, stringEsc
		case b == 0x07 && t.st == oscString, b == 0x18, b == 0x1a:
			t.st = ground
		}
	case stringEsc:
		if b == '\\' {
			t.st = ground
			return
		}
		// Not a string terminator: the ESC starts a new sequence.
		t.st = escape
		t.stepEscape(b)
	}
}

func (t *Tracker) stepGround(b byte) {
	if b == 0x1b {
		t.utf8Left = 0
		t.st = escape
		return
	}
	if t.utf8Left > 0 && b >= 0x80 && b <= 0xbf {
		t.utf8Left--
		return
	}
	switch {
	case b >= 0xc2 && b <= 0xdf:
		t.utf8Left = 1
	case b >= 0xe0 && b <= 0xef:
		t.utf8Left = 2
	case b >= 0xf0 && b <= 0xf4:
		t.utf8Left = 3
	default:
		t.utf8Left = 0
	}
}

func (t *Tracker) stepEscape(b byte) {
	t.st = ground
	switch {
	case b == '[':
		t.st = csi
		t.prefix, t.inter, t.interN, t.nparams = 0, 0, 0, 0
	case b == ']':
		t.st = oscString
	case b == 'P', b == 'X', b == '^', b == '_':
		t.st = string_
	case b == 0x1b:
		t.st = escape
	case b >= 0x20 && b <= 0x2f:
		t.st = escapeMid
	case b == 'c': // RIS, full reset
		t.reset()
	case b == '=':
		t.keypadApp = true
	case b == '>':
		t.keypadApp = false
	}
}

func (t *Tracker) stepCSI(b byte) {
	switch {
	case b >= '0' && b <= '9':
		if t.nparams == 0 {
			t.nparams = 1
			t.params[0] = 0
		}
		if i := t.nparams - 1; i < maxParams && t.params[i] < 1<<20 {
			t.params[i] = t.params[i]*10 + int(b-'0')
		}
	case b == ';' || b == ':':
		if t.nparams == 0 {
			t.nparams = 1
			t.params[0] = 0
		}
		if t.nparams < maxParams {
			t.params[t.nparams] = 0
		}
		t.nparams++
	case b >= '<' && b <= '?':
		if t.nparams == 0 && t.prefix == 0 {
			t.prefix = b
		}
	case b >= 0x20 && b <= 0x2f:
		t.inter = b
		t.interN++
	case b >= 0x40 && b <= 0x7e:
		t.st = ground
		t.dispatch(b)
	case b == 0x1b:
		t.st = escape
	case b == 0x18 || b == 0x1a:
		t.st = ground
	}
}

// param returns parameter i, or def when it is missing or zero.
func (t *Tracker) param(i, def int) int {
	if i >= t.nparams || i >= maxParams || t.params[i] == 0 {
		return def
	}
	return t.params[i]
}

func (t *Tracker) dispatch(final byte) {
	n := min(t.nparams, maxParams)

	switch {
	case t.prefix == '?' && t.interN == 0 && (final == 'h' || final == 'l'):
		for _, mode := range t.params[:n] {
			t.setMode(mode, final == 'h')
		}

	case t.prefix == 0 && t.interN == 0 && final == 'c':
		if n == 0 || (n == 1 && t.params[0] == 0) {
			t.primaryDA++
		}

	case t.prefix == '>' && t.interN == 0 && final == 'u': // push keyboard flags
		if len(t.kitty) < maxKittyDeep {
			t.kitty = append(t.kitty, t.param(0, 0))
		}
	case t.prefix == '<' && t.interN == 0 && final == 'u': // pop keyboard flags
		pop := min(t.param(0, 1), len(t.kitty))
		t.kitty = t.kitty[:len(t.kitty)-pop]
	case t.prefix == '=' && t.interN == 0 && final == 'u': // set keyboard flags
		if len(t.kitty) == 0 {
			t.kitty = append(t.kitty, 0)
		}
		top := &t.kitty[len(t.kitty)-1]
		switch flags := t.param(0, 0); t.param(1, 1) {
		case 1:
			*top = flags
		case 2:
			*top |= flags
		case 3:
			*top &^= flags
		}

	case t.prefix == '>' && t.interN == 0 && final == 'm': // modifyOtherKeys
		if t.param(0, 0) == 4 {
			t.otherKeys = t.param(1, 0)
			t.otherKeysOn = true
		}

	case t.prefix == 0 && t.interN == 1 && t.inter == ' ' && final == 'q': // cursor style
		t.cursorStyle = t.param(0, 0)
		t.cursorSet = true

	case t.prefix == 0 && t.interN == 1 && t.inter == '!' && final == 'p': // soft reset
		t.setMode(1, false)
		t.setMode(6, false)
		t.setMode(7, true)
		t.setMode(25, true)
		t.keypadApp = false
	}
}

func (t *Tracker) setMode(mode int, on bool) {
	if t.modes == nil {
		t.modes = make(map[int]bool)
	}
	if _, known := t.modes[mode]; !known && len(t.modes) >= maxModes {
		return
	}
	t.modes[mode] = on
}

func (t *Tracker) reset() {
	clear(t.modes)
	t.keypadApp = false
	t.kitty = t.kitty[:0]
	t.otherKeys, t.otherKeysOn = 0, false
	t.cursorStyle, t.cursorSet = 0, false
}

// Restore returns the bytes that put a freshly reset terminal into the modes
// in effect at this point of the stream. It restores terminal modes, the
// keypad mode, keyboard protocol flags and the cursor style. It does not
// restore the screen contents, the colours in effect or the scroll region.
func (t *Tracker) Restore() []byte {
	var out []byte

	modes := make([]int, 0, len(t.modes))
	for mode := range t.modes {
		modes = append(modes, mode)
	}
	sort.Ints(modes)
	for _, mode := range modes {
		out = append(out, "\x1b[?"...)
		out = strconv.AppendInt(out, int64(mode), 10)
		if t.modes[mode] {
			out = append(out, 'h')
		} else {
			out = append(out, 'l')
		}
	}

	if t.keypadApp {
		out = append(out, "\x1b="...)
	}
	for _, flags := range t.kitty {
		out = append(out, "\x1b[>"...)
		out = strconv.AppendInt(out, int64(flags), 10)
		out = append(out, 'u')
	}
	if t.otherKeysOn {
		out = append(out, "\x1b[>4;"...)
		out = strconv.AppendInt(out, int64(t.otherKeys), 10)
		out = append(out, 'm')
	}
	if t.cursorSet {
		out = append(out, "\x1b["...)
		out = strconv.AppendInt(out, int64(t.cursorStyle), 10)
		out = append(out, " q"...)
	}
	return out
}
