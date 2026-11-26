// Package ptytest lets a test binary act as the program inside a PTY, so tests
// run the same small program on every platform.
//
// A test package calls RunHelper first thing in TestMain, and starts sessions
// with Command.
package ptytest

import (
	"bufio"
	"fmt"
	"os"
	"time"
)

const envMode = "WORKY_PTYTEST_MODE"

// Modes of the helper program.
const (
	// Echo prints "ready", then answers each input line with got:"<line>".
	// The line "exit" ends it with exit code 3.
	Echo = "echo"
	// AskDeviceAttributes sends a primary device attributes query, then
	// behaves like Echo.
	AskDeviceAttributes = "ask-da"
)

// In either mode the input line FloodLine makes the helper print at least
// FloodSize bytes and then a line holding FloodDone.
const (
	FloodLine = "flood"
	FloodDone = "flood-done"
	FloodSize = 4 << 20
)

// The input line LastWordsLine makes the helper go silent for a moment, print
// LastWords and exit straight away.
const (
	LastWordsLine = "bye"
	LastWords     = "last-words"
)

// RunHelper turns the process into the helper program when it was started
// through Command. It does not return in that case.
func RunHelper() {
	mode := os.Getenv(envMode)
	if mode == "" {
		return
	}
	if mode == AskDeviceAttributes {
		fmt.Print("\x1b[c")
	}
	fmt.Println("ready")

	lines := bufio.NewScanner(os.Stdin)
	for lines.Scan() {
		switch lines.Text() {
		case "exit":
			os.Exit(3)
		case FloodLine:
			flood()
		case LastWordsLine:
			// Silence first, so the words are all the reader has left to
			// collect when the program is gone.
			time.Sleep(300 * time.Millisecond)
			fmt.Println(LastWords)
			os.Exit(0)
		default:
			fmt.Printf("got:%q\n", lines.Text())
		}
	}
	os.Exit(0)
}

// flood prints FloodSize bytes of numbered lines, then FloodDone.
func flood() {
	out := bufio.NewWriterSize(os.Stdout, 64<<10)
	const filler = "the quick brown fox jumps over the lazy dog 0123456789"
	for written, n := 0, 0; written < FloodSize; n++ {
		line := fmt.Sprintf("%08d %s\n", n, filler)
		out.WriteString(line)
		written += len(line)
	}
	out.WriteString(FloodDone + "\n")
	out.Flush()
}

// Command returns the command and environment that run the helper program in
// the given mode.
func Command(mode string) (argv []string, env map[string]string) {
	return []string{os.Args[0]}, map[string]string{envMode: mode}
}
