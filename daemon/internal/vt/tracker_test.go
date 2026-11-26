package vt

import "testing"

func TestSafe(t *testing.T) {
	tests := []struct {
		name string
		in   string
		safe bool
	}{
		{"empty", "", true},
		{"plain text", "hello\r\n", true},
		{"lone escape", "\x1b", false},
		{"unfinished control sequence", "\x1b[38;5", false},
		{"finished control sequence", "\x1b[38;5;1m", true},
		{"first byte of a three-byte character", "\xed", false},
		{"two bytes of a three-byte character", "\xed\x95", false},
		{"whole three-byte character", "\xed\x95\x9c", true},
		{"unfinished title string", "\x1b]0;title", false},
		{"title string ended by BEL", "\x1b]0;title\x07", true},
		{"title string ended by ST", "\x1b]0;title\x1b\\", true},
		{"title string with half a terminator", "\x1b]0;title\x1b", false},
		{"unfinished device control string", "\x1bPq#0", false},
		{"device control string is not ended by BEL", "\x1bPq\x07", false},
		{"device control string ended by ST", "\x1bPq#0\x1b\\", true},
		{"charset selection", "\x1b(B", true},
		{"unfinished charset selection", "\x1b(", false},
		{"cancelled control sequence", "\x1b[12\x18", true},
		{"escape restarts a sequence", "\x1b[12\x1b[0m", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tr Tracker
			tr.Write([]byte(tt.in))
			if got := tr.Safe(); got != tt.safe {
				t.Fatalf("Safe() = %v, want %v", got, tt.safe)
			}
		})
	}
}

func TestRestore(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"nothing set", "plain", ""},
		{"bracketed paste on", "\x1b[?2004h", "\x1b[?2004h"},
		{"mode turned off again keeps the explicit off", "\x1b[?2004h\x1b[?2004l", "\x1b[?2004l"},
		{"several modes in one sequence, sorted", "\x1b[?1049;1006;1000h", "\x1b[?1000h\x1b[?1006h\x1b[?1049h"},
		{"hidden cursor", "\x1b[?25l", "\x1b[?25l"},
		{"non-private set mode is ignored", "\x1b[4h", ""},
		{"application keypad", "\x1b=", "\x1b="},
		{"application keypad turned off", "\x1b=\x1b>", ""},
		{"keyboard flags pushed", "\x1b[>1u", "\x1b[>1u"},
		{"keyboard flags pushed twice, popped once", "\x1b[>1u\x1b[>3u\x1b[<u", "\x1b[>1u"},
		{"keyboard flags pop more than pushed", "\x1b[>1u\x1b[<5u", ""},
		{"keyboard flags set", "\x1b[>1u\x1b[=8;2u", "\x1b[>9u"},
		{"keyboard flags cleared bit", "\x1b[>11u\x1b[=2;3u", "\x1b[>9u"},
		{"keyboard flag query is ignored", "\x1b[?u", ""},
		{"modifyOtherKeys", "\x1b[>4;2m", "\x1b[>4;2m"},
		{"modifyOtherKeys reset stays explicit", "\x1b[>4;2m\x1b[>4m", "\x1b[>4;0m"},
		{"colour change is not restored", "\x1b[31m", ""},
		{"cursor style", "\x1b[5 q", "\x1b[5 q"},
		{"full reset clears everything", "\x1b[?2004h\x1b[>1u\x1b=\x1bc", ""},
		{"soft reset", "\x1b[?1h\x1b[!p", "\x1b[?1l\x1b[?6l\x1b[?7h\x1b[?25h"},
		{"an escape inside a string ends the string", "\x1b]0;\x1b[?2004h\x07", "\x1b[?2004h"},
		{"sequence split across text", "\x1b[?20", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tr Tracker
			tr.Write([]byte(tt.in))
			if got := string(tr.Restore()); got != tt.want {
				t.Fatalf("Restore() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWriteSplitAnywhere(t *testing.T) {
	in := "a\x1b[?2004h한\x1b]0;t\x1b\\\x1b[>5u\x1b[?1049;25h\x1b[?25l"
	var whole Tracker
	whole.Write([]byte(in))
	want := string(whole.Restore())

	for i := range len(in) {
		var tr Tracker
		tr.Write([]byte(in[:i]))
		tr.Write([]byte(in[i:]))
		if got := string(tr.Restore()); got != want {
			t.Fatalf("split at %d: Restore() = %q, want %q", i, got, want)
		}
		if !tr.Safe() {
			t.Fatalf("split at %d: not safe at the end", i)
		}
	}
}

func TestPrimaryDA(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"none", "text", 0},
		{"bare query", "\x1b[c", 1},
		{"query with zero", "\x1b[0c", 1},
		{"secondary query is not counted", "\x1b[>c", 0},
		{"tertiary query is not counted", "\x1b[=c", 0},
		{"other parameter is not counted", "\x1b[1c", 0},
		{"two queries", "\x1b[c..\x1b[0c", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tr Tracker
			tr.Write([]byte(tt.in))
			if got := tr.TakePrimaryDA(); got != tt.want {
				t.Fatalf("TakePrimaryDA() = %d, want %d", got, tt.want)
			}
			if got := tr.TakePrimaryDA(); got != 0 {
				t.Fatalf("second TakePrimaryDA() = %d, want 0", got)
			}
		})
	}
}
