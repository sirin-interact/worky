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
		if lines.Text() == "exit" {
			os.Exit(3)
		}
		fmt.Printf("got:%q\n", lines.Text())
	}
	os.Exit(0)
}

// Command returns the command and environment that run the helper program in
// the given mode.
func Command(mode string) (argv []string, env map[string]string) {
	return []string{os.Args[0]}, map[string]string{envMode: mode}
}
