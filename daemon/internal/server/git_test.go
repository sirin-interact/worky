package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sirin-interact/worky/daemon/internal/protocol"
)

func TestSearchAndGit(t *testing.T) {
	// Commit runs git as the machine has it set up, and a test machine has
	// not told git who its user is.
	config := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(config, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(key, "Test")
	}
	for _, key := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(key, "test@example.com")
	}

	d := startDaemon(t)
	c := connect(t, d)
	repo := newRepo(t, d, "app")
	if err := os.WriteFile(filepath.Join(repo, "plan.md"), []byte("# 계획\nrollup\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var found protocol.FsSearchResult
	c.mustCall(protocol.MethodFsSearch, protocol.FsSearchParams{Root: repo, Query: "계획", Kind: protocol.SearchContent}, &found)
	if len(found.Matches) != 1 || found.Matches[0].Path != "plan.md" || found.Matches[0].Line != 1 {
		t.Errorf("content search: %+v", found)
	}
	found = protocol.FsSearchResult{}
	c.mustCall(protocol.MethodFsSearch, protocol.FsSearchParams{Root: repo, Query: "PLAN", Kind: protocol.SearchName}, &found)
	if len(found.Files) != 1 || found.Files[0] != "plan.md" {
		t.Errorf("name search: %+v", found)
	}

	var status protocol.GitStatusResult
	c.mustCall(protocol.MethodGitStatus, protocol.GitPathParams{Path: repo}, &status)
	if !sameDir(status.Root, repo) || status.Branch == nil || *status.Branch != "main" {
		t.Errorf("status: %+v", status)
	}
	if len(status.Files) != 1 || status.Files[0].Path != "plan.md" || status.Files[0].Index != "?" || status.Files[0].Added != 2 {
		t.Errorf("changed files: %+v", status.Files)
	}

	c.mustCall(protocol.MethodGitStage, protocol.GitFilesParams{Path: repo, Files: []string{"plan.md"}}, nil)
	c.mustCall(protocol.MethodGitStatus, protocol.GitPathParams{Path: repo}, &status)
	if len(status.Files) != 1 || status.Files[0].Index != "A" {
		t.Errorf("after staging: %+v", status.Files)
	}
	c.mustCall(protocol.MethodGitUnstage, protocol.GitFilesParams{Path: repo, Files: []string{"plan.md"}}, nil)
	c.mustCall(protocol.MethodGitStage, protocol.GitFilesParams{Path: repo, Files: []string{"plan.md"}}, nil)

	var commit protocol.GitCommitResult
	c.mustCall(protocol.MethodGitCommit, protocol.GitCommitParams{Path: repo, Message: "docs: add the plan"}, &commit)
	if commit.Head == "" || commit.Head == status.Head {
		t.Errorf("the commit did not move HEAD: %q", commit.Head)
	}
	status = protocol.GitStatusResult{}
	c.mustCall(protocol.MethodGitStatus, protocol.GitPathParams{Path: repo}, &status)
	if len(status.Files) != 0 || status.Head != commit.Head {
		t.Errorf("after the commit: %+v", status)
	}

	wantError(t, "status outside a repository", c.call(protocol.MethodGitStatus, protocol.GitPathParams{Path: filepath.Dir(repo)}, nil), protocol.ErrNotARepository)
	wantError(t, "a commit with nothing staged", c.call(protocol.MethodGitCommit, protocol.GitCommitParams{Path: repo, Message: "x"}, nil), protocol.ErrGitFailed)
	wantError(t, "a search with a relative root", c.call(protocol.MethodFsSearch, protocol.FsSearchParams{Root: "app", Query: "x", Kind: protocol.SearchName}, nil), protocol.ErrInvalidRequest)
}

func TestSessionReportsActivity(t *testing.T) {
	d := startDaemon(t)
	c := connect(t, d)
	s := c.createEcho(d)
	if s.Activity != protocol.ActivityBusy || s.ActivityAt == "" {
		t.Errorf("a new session: activity %q at %q", s.Activity, s.ActivityAt)
	}
	// Once its first words are out the program waits for input, and every
	// client hears that it went quiet.
	idle := c.waitSession(protocol.EventSessionUpdated, func(got protocol.Session) bool {
		return got.ID == s.ID && got.Activity == protocol.ActivityIdle
	})
	if idle.ActivityAt == s.ActivityAt {
		t.Error("going idle did not move the activity time")
	}
}
