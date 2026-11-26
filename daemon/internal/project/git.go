package project

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirin-interact/worky/daemon/internal/protocol"
)

const (
	// How long git may take to answer a question.
	queryLimit = 15 * time.Second
	// How long git may take to check out or delete a worktree.
	checkoutLimit = 15 * time.Minute
)

// Variables that would point git at another repository than the one asked
// about.
var gitLocation = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_PREFIX"}

// gitError is a failed git command.
type gitError struct {
	stderr string
	err    error
}

func (e *gitError) Error() string {
	if e.stderr != "" {
		return e.stderr
	}
	return e.err.Error()
}

func (e *gitError) Unwrap() error { return e.err }

// protocolError turns a git failure into what a client is told.
func protocolError(err error) *protocol.Error {
	var ge *gitError
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return protocol.Errorf(protocol.ErrGitFailed, "git is not installed on this machine")
	case errors.As(err, &ge) && strings.Contains(ge.stderr, "not a git repository"):
		return protocol.Errorf(protocol.ErrNotARepository, "not in a git repository")
	}
	return protocol.Errorf(protocol.ErrGitFailed, "%v", err)
}

func gitEnv() []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		skip := false
		for _, location := range gitLocation {
			skip = skip || strings.EqualFold(name, location)
		}
		if !skip {
			env = append(env, entry)
		}
	}
	// No prompts, no background index refresh, and messages this package can
	// recognise.
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
}

// git runs git in dir and returns what it printed.
func git(limit time.Duration, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	cmd.SysProcAttr = sysProcAttr()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return "", &gitError{stderr: strings.TrimSpace(stderr.String()), err: err}
	}
	return stdout.String(), nil
}

// succeeds reports whether a git command exits with status 0.
func succeeds(dir string, args ...string) bool {
	_, err := git(queryLimit, dir, args...)
	return err == nil
}

// listing is what git says about a repository's worktrees.
type listing struct {
	// main is the main worktree, or the repository itself when it is bare.
	main      string
	worktrees []protocol.Worktree
}

// listWorktrees asks git for the worktrees of the repository dir is in.
// Worktrees whose directory is missing are left out.
func listWorktrees(dir string) (listing, error) {
	out, err := git(queryLimit, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return listing{}, err
	}
	return parseWorktrees(out, func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && info.IsDir()
	}), nil
}

// parseWorktrees reads the output of git worktree list --porcelain: one
// block of lines per worktree, the main one first, with a blank line between
// blocks.
func parseWorktrees(out string, exists func(path string) bool) listing {
	var l listing
	first := true
	for block := range strings.SplitSeq(strings.ReplaceAll(out, "\r\n", "\n"), "\n\n") {
		var wt protocol.Worktree
		bare := false
		for line := range strings.SplitSeq(block, "\n") {
			name, value, _ := strings.Cut(line, " ")
			switch name {
			case "worktree":
				wt.Path = filepath.Clean(filepath.FromSlash(value))
			case "branch":
				branch := strings.TrimPrefix(value, "refs/heads/")
				wt.Branch = &branch
			case "bare":
				bare = true
			}
		}
		if wt.Path == "" {
			continue
		}
		if first {
			l.main, wt.Main = wt.Path, !bare
			first = false
		}
		if !bare && exists(wt.Path) {
			l.worktrees = append(l.worktrees, wt)
		}
	}
	return l
}

// commonDir returns the directory that holds a repository's own files, which
// is .git in the main worktree unless the repository is bare.
func commonDir(repo string) (string, error) {
	out, err := git(queryLimit, repo, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	dir := filepath.FromSlash(strings.TrimSpace(out))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repo, dir)
	}
	return filepath.Clean(dir), nil
}

// dirName turns a branch name into a name for a directory.
func dirName(branch string) string {
	var b strings.Builder
	for _, r := range branch {
		if r <= ' ' || strings.ContainsRune(`/\:*?"<>|`, r) {
			b.WriteByte('-')
		} else {
			b.WriteRune(r)
		}
	}
	if name := strings.Trim(b.String(), "-."); name != "" {
		return name
	}
	return "worktree"
}
