package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sirin-interact/worky/daemon/internal/protocol"
	"github.com/sirin-interact/worky/daemon/internal/ptytest"
)

// newRepo makes a git repository with one commit, in the daemon's work
// directory so that it outlives the sessions and watches inside it.
func newRepo(t *testing.T, d *testDaemon, name string) string {
	t.Helper()
	work, err := filepath.EvalSymlinks(d.workDir)
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(work, name)
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"add", "."},
		{"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "first"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return repo
}

func sameDir(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

// waitEvent waits for an event whose data satisfies cond, and returns the
// data.
func (c *client) waitEvent(event string, cond func(data json.RawMessage) bool) json.RawMessage {
	c.t.Helper()
	var found json.RawMessage
	poll(c.t, event, func() (bool, string) {
		c.mu.Lock()
		events := slices.Clone(c.events)
		c.mu.Unlock()
		var names []string
		for _, ev := range events {
			names = append(names, ev.Event)
			if ev.Event == event && cond(ev.Data) {
				found = ev.Data
				return true, ""
			}
		}
		return false, fmt.Sprintf("events so far: %q", names)
	})
	return found
}

// project returns a project as the events received so far leave it.
func (c *client) project(id string) protocol.Project {
	c.mu.Lock()
	defer c.mu.Unlock()
	var latest protocol.Project
	for _, ev := range c.events {
		var result protocol.ProjectResult
		if json.Unmarshal(ev.Data, &result) == nil && result.Project.ID == id &&
			(ev.Event == protocol.EventProjectAdded || ev.Event == protocol.EventProjectUpdated) {
			latest = result.Project
		}
	}
	return latest
}

// waitProject waits for an event carrying a project that satisfies cond.
func (c *client) waitProject(event string, cond func(protocol.Project) bool) protocol.Project {
	c.t.Helper()
	var found protocol.Project
	c.waitEvent(event, func(data json.RawMessage) bool {
		var result protocol.ProjectResult
		if json.Unmarshal(data, &result) != nil || !cond(result.Project) {
			return false
		}
		found = result.Project
		return true
	})
	return found
}

func (c *client) count(event string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, ev := range c.events {
		if ev.Event == event {
			n++
		}
	}
	return n
}

func (c *client) createIn(cwd string) protocol.Session {
	c.t.Helper()
	argv, env := ptytest.Command(ptytest.Echo)
	var res protocol.SessionResult
	c.mustCall(protocol.MethodSessionCreate, protocol.SessionCreateParams{
		Cwd: cwd, Cols: 80, Rows: 24, Command: argv, Env: env,
	}, &res)
	return res.Session
}

func worktreeOf(s protocol.Session) string {
	if s.Worktree == nil {
		return "<none>"
	}
	return *s.Worktree
}

func wantError(t *testing.T, what string, perr *protocol.Error, code protocol.ErrorCode) {
	t.Helper()
	if perr == nil || perr.Code != code {
		t.Errorf("%s: %v, want %s", what, perr, code)
	}
}

func TestProjectsAndWorktrees(t *testing.T) {
	d := startDaemon(t)
	alice, bob := connect(t, d), connect(t, d)
	repo := newRepo(t, d, "app")

	// Every client hears of a project one of them adds.
	var added protocol.ProjectResult
	alice.mustCall(protocol.MethodProjectAdd, protocol.ProjectAddParams{Path: repo}, &added)
	p := added.Project
	if p.Name != "app" || !sameDir(p.Path, repo) || len(p.Worktrees) != 1 || !p.Worktrees[0].Main {
		t.Fatalf("project = %+v", p)
	}
	bob.waitProject(protocol.EventProjectAdded, func(got protocol.Project) bool { return got.ID == p.ID })

	var list protocol.ProjectListResult
	bob.mustCall(protocol.MethodProjectList, nil, &list)
	if len(list.Projects) != 1 || list.Projects[0].ID != p.ID {
		t.Errorf("list = %+v", list)
	}

	bob.mustCall(protocol.MethodProjectRename, protocol.ProjectRenameParams{ProjectID: p.ID, Name: "Shop"}, nil)
	alice.waitProject(protocol.EventProjectUpdated, func(got protocol.Project) bool { return got.Name == "Shop" })

	// A worktree, and sessions that say they are in it.
	var created protocol.WorktreeResult
	bob.mustCall(protocol.MethodWorktreeCreate, protocol.WorktreeCreateParams{ProjectID: p.ID, Branch: "fix"}, &created)
	wt := created.Worktree
	if wt.Branch == nil || *wt.Branch != "fix" || wt.Main {
		t.Fatalf("worktree = %+v", wt)
	}
	alice.waitProject(protocol.EventProjectUpdated, func(got protocol.Project) bool {
		return len(got.Worktrees) == 2 && got.Worktrees[1].Path == wt.Path
	})

	inner := filepath.Join(wt.Path, "src")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	inWorktree, inMain, outside := alice.createIn(inner), alice.createIn(p.Path), alice.createIn(d.workDir)
	if worktreeOf(inWorktree) != wt.Path || worktreeOf(inMain) != p.Path || worktreeOf(outside) != "<none>" {
		t.Errorf("sessions are in %q, %q and %q", worktreeOf(inWorktree), worktreeOf(inMain), worktreeOf(outside))
	}

	// A worktree with a session in it stays.
	remove := protocol.WorktreeRemoveParams{ProjectID: p.ID, Path: wt.Path, Force: true}
	wantError(t, "removing a worktree in use", bob.call(protocol.MethodWorktreeRemove, remove, nil), protocol.ErrInUse)
	alice.mustCall(protocol.MethodSessionClose, protocol.SessionIDParams{SessionID: inWorktree.ID}, nil)
	bob.mustCall(protocol.MethodWorktreeRemove, remove, nil)
	if _, err := os.Stat(wt.Path); err == nil {
		t.Error("the worktree's directory is still there")
	}
	poll(t, "the project to have one worktree again", func() (bool, string) {
		got := alice.project(p.ID)
		return len(got.Worktrees) == 1 && got.Worktrees[0].Main, fmt.Sprintf("worktrees: %+v", got.Worktrees)
	})

	// Forgetting the project leaves its sessions running, in no worktree.
	bob.mustCall(protocol.MethodProjectRemove, protocol.ProjectIDParams{ProjectID: p.ID}, nil)
	alice.waitEvent(protocol.EventProjectRemoved, func(data json.RawMessage) bool {
		var removed protocol.ProjectIDParams
		return json.Unmarshal(data, &removed) == nil && removed.ProjectID == p.ID
	})
	left := alice.waitSession(protocol.EventSessionUpdated, func(s protocol.Session) bool {
		return s.ID == inMain.ID && s.Worktree == nil
	})
	if left.Status != protocol.StatusRunning {
		t.Errorf("the session is %s", left.Status)
	}

	wantError(t, "an unknown project", bob.call(protocol.MethodProjectRemove, protocol.ProjectIDParams{ProjectID: p.ID}, nil), protocol.ErrNotFound)
	wantError(t, "a directory outside git", bob.call(protocol.MethodProjectAdd, protocol.ProjectAddParams{Path: d.workDir}, nil), protocol.ErrNotARepository)
}

// A session started before its directory became a project is matched to the
// worktree when the project is added.
func TestSessionJoinsProjectAddedLater(t *testing.T) {
	d := startDaemon(t)
	c := connect(t, d)
	repo := newRepo(t, d, "app")

	s := c.createIn(repo)
	if s.Worktree != nil {
		t.Fatalf("worktree = %q before any project exists", *s.Worktree)
	}
	var added protocol.ProjectResult
	c.mustCall(protocol.MethodProjectAdd, protocol.ProjectAddParams{Path: repo}, &added)
	c.waitSession(protocol.EventSessionUpdated, func(got protocol.Session) bool {
		return got.ID == s.ID && got.Worktree != nil && *got.Worktree == added.Project.Path
	})
}

func TestFiles(t *testing.T) {
	d := startDaemon(t)
	c := connect(t, d)
	dir := filepath.Join(d.workDir, "files")

	var roots protocol.FsRootsResult
	c.mustCall(protocol.MethodFsRoots, nil, &roots)
	if len(roots.Roots) == 0 || roots.Home == "" {
		t.Errorf("roots = %+v", roots)
	}

	c.mustCall(protocol.MethodFsMkdir, protocol.FsPathParams{Path: filepath.Join(dir, "sub")}, nil)

	// Content is base64 on the wire, whatever the bytes are.
	content := []byte{0xff, 0x00, 'h', 'i', '\r', '\n', 0xea, 0xb0, 0x80}
	file := filepath.Join(dir, "sub", "f.bin")
	var written protocol.FsWriteResult
	c.mustCall(protocol.MethodFsWrite, map[string]any{
		"path": file, "data": base64.StdEncoding.EncodeToString(content), "exclusive": true,
	}, &written)
	if onDisk, _ := os.ReadFile(file); string(onDisk) != string(content) {
		t.Errorf("the file holds %q", onDisk)
	}
	var read struct {
		Data    string `json:"data"`
		Size    int64  `json:"size"`
		Version string `json:"version"`
	}
	c.mustCall(protocol.MethodFsRead, protocol.FsReadParams{Path: file}, &read)
	if read.Data != base64.StdEncoding.EncodeToString(content) || read.Size != int64(len(content)) || read.Version != written.Version {
		t.Errorf("read = %+v, written = %+v", read, written)
	}

	var st protocol.FsStatResult
	c.mustCall(protocol.MethodFsStat, protocol.FsPathParams{Path: file}, &st)
	if st.Type != protocol.TypeFile || st.Size != int64(len(content)) || st.Version != written.Version {
		t.Errorf("stat = %+v", st)
	}

	// An editor that saves over a file somebody else changed is told.
	if err := os.WriteFile(file, []byte("an agent was here"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := protocol.FsWriteParams{Path: file, Data: []byte("mine"), Expect: &written.Version}
	wantError(t, "saving over a changed file", c.call(protocol.MethodFsWrite, stale, nil), protocol.ErrConflict)

	c.mustCall(protocol.MethodFsRename, protocol.FsRenameParams{From: file, To: filepath.Join(dir, "g.bin")}, nil)
	var list protocol.FsListResult
	c.mustCall(protocol.MethodFsList, protocol.FsPathParams{Path: dir}, &list)
	if len(list.Entries) != 2 || list.Entries[0].Name != "g.bin" || list.Entries[1].Name != "sub" || list.Entries[1].Type != protocol.TypeDir {
		t.Errorf("list = %+v", list)
	}

	c.mustCall(protocol.MethodFsRemove, protocol.FsRemoveParams{Path: dir, Recursive: true}, nil)
	wantError(t, "listing what was removed", c.call(protocol.MethodFsList, protocol.FsPathParams{Path: dir}, nil), protocol.ErrNotFound)
	wantError(t, "a relative path", c.call(protocol.MethodFsStat, protocol.FsPathParams{Path: "f"}, nil), protocol.ErrInvalidRequest)
	wantError(t, "malformed params", c.call(protocol.MethodFsRead, map[string]any{"path": 7}, nil), protocol.ErrInvalidRequest)
}

// changes returns the fs.changed events the client has received for a watch.
func (c *client) changes(watch uint32) (names []string, unknown, ended bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ev := range c.events {
		if ev.Event != protocol.EventFsChanged {
			continue
		}
		var change protocol.FsChanged
		if json.Unmarshal(ev.Data, &change) != nil || change.Watch != watch {
			continue
		}
		names = append(names, change.Names...)
		unknown = unknown || (change.Names == nil && !change.Ended)
		ended = ended || change.Ended
	}
	return names, unknown, ended
}

func (c *client) waitChange(watch uint32, name string) {
	c.t.Helper()
	poll(c.t, "a change to "+name, func() (bool, string) {
		names, _, _ := c.changes(watch)
		return slices.Contains(names, name), fmt.Sprintf("names so far: %q", names)
	})
}

func TestWatch(t *testing.T) {
	d := startDaemon(t)
	alice, bob := connect(t, d), connect(t, d)
	dir := filepath.Join(d.workDir, "watched")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	var top, sub, again protocol.FsWatchResult
	alice.mustCall(protocol.MethodFsWatch, protocol.FsPathParams{Path: dir}, &top)
	alice.mustCall(protocol.MethodFsWatch, protocol.FsPathParams{Path: filepath.Join(dir, "sub")}, &sub)
	alice.mustCall(protocol.MethodFsWatch, protocol.FsPathParams{Path: dir}, &again)
	if top.Watch == sub.Watch || top.Watch == again.Watch {
		t.Fatalf("watch ids %d, %d and %d", top.Watch, sub.Watch, again.Watch)
	}

	// What another client writes, and what a program on the machine writes.
	bob.mustCall(protocol.MethodFsWrite, protocol.FsWriteParams{Path: filepath.Join(dir, "a.txt"), Data: []byte("a")}, nil)
	alice.waitChange(top.Watch, "a.txt")
	alice.waitChange(again.Watch, "a.txt")
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	alice.waitChange(sub.Watch, "b.txt")
	if names, _, _ := alice.changes(top.Watch); slices.Contains(names, "b.txt") {
		t.Error("a change below the directory was reported by name")
	}
	if bob.count(protocol.EventFsChanged) != 0 {
		t.Error("a client that watches nothing heard of a change")
	}

	// A watch that is ended says no more.
	alice.mustCall(protocol.MethodFsUnwatch, protocol.FsUnwatchParams{Watch: again.Watch}, nil)
	wantError(t, "ending a watch twice", alice.call(protocol.MethodFsUnwatch, protocol.FsUnwatchParams{Watch: again.Watch}, nil), protocol.ErrNotFound)
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	alice.waitChange(top.Watch, "c.txt")
	time.Sleep(300 * time.Millisecond)
	if names, _, _ := alice.changes(again.Watch); slices.Contains(names, "c.txt") {
		t.Error("an ended watch reported a change")
	}

	// The directory goes.
	if err := os.RemoveAll(filepath.Join(dir, "sub")); err != nil {
		t.Fatal(err)
	}
	poll(t, "the end of the watch", func() (bool, string) {
		_, _, ended := alice.changes(sub.Watch)
		return ended, ""
	})
	alice.waitChange(top.Watch, "sub")
	alice.mustCall(protocol.MethodFsUnwatch, protocol.FsUnwatchParams{Watch: sub.Watch}, nil)

	wantError(t, "watching a file", alice.call(protocol.MethodFsWatch, protocol.FsPathParams{Path: filepath.Join(dir, "a.txt")}, nil), protocol.ErrInvalidRequest)
	wantError(t, "watching nothing", alice.call(protocol.MethodFsWatch, protocol.FsPathParams{Path: filepath.Join(dir, "sub")}, nil), protocol.ErrNotFound)
}

// Watches inside a worktree let its directories be renamed and the worktree
// be removed.
func TestWatchLeavesDirectoriesFree(t *testing.T) {
	d := startDaemon(t)
	c := connect(t, d)
	repo := newRepo(t, d, "app")
	var added protocol.ProjectResult
	c.mustCall(protocol.MethodProjectAdd, protocol.ProjectAddParams{Path: repo}, &added)
	var created protocol.WorktreeResult
	c.mustCall(protocol.MethodWorktreeCreate, protocol.WorktreeCreateParams{ProjectID: added.Project.ID, Branch: "fix"}, &created)
	wt := created.Worktree.Path

	deep := filepath.Join(wt, "src", "ui", "button")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	var top, inner protocol.FsWatchResult
	c.mustCall(protocol.MethodFsWatch, protocol.FsPathParams{Path: wt}, &top)
	c.mustCall(protocol.MethodFsWatch, protocol.FsPathParams{Path: deep}, &inner)

	if err := os.WriteFile(filepath.Join(deep, "index.ts"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.waitChange(inner.Watch, "index.ts")

	// As an agent would, with a directory two levels down being watched.
	if err := os.Rename(filepath.Join(wt, "src"), filepath.Join(wt, "lib")); err != nil {
		t.Fatalf("renaming a directory above a watched one: %v", err)
	}
	c.waitChange(top.Watch, "lib")
	poll(t, "the end of the watch on the directory that moved", func() (bool, string) {
		_, _, ended := c.changes(inner.Watch)
		return ended, ""
	})

	c.mustCall(protocol.MethodWorktreeRemove, protocol.WorktreeRemoveParams{ProjectID: added.Project.ID, Path: wt, Force: true}, nil)
	poll(t, "the worktree's directory to go", func() (bool, string) {
		_, err := os.Stat(wt)
		return err != nil, ""
	})
}

// More requests at once than run at once are all answered.
func TestManyRequestsAtOnce(t *testing.T) {
	d := startDaemon(t)
	c := connect(t, d)
	file := filepath.Join(d.workDir, "f.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	const n = 10 * maxBackground
	var wg sync.WaitGroup
	failures := make(chan string, n)
	for range n {
		wg.Go(func() {
			var read protocol.FsReadResult
			if perr := c.call(protocol.MethodFsRead, protocol.FsReadParams{Path: file}, &read); perr != nil || string(read.Data) != "hello" {
				failures <- fmt.Sprintf("read = %q, %v", read.Data, perr)
			}
		})
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}

	// The connection still answers in-order requests.
	var list protocol.SessionListResult
	c.mustCall(protocol.MethodSessionList, nil, &list)
}
