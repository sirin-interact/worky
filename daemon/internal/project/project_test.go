package project

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirin-interact/worky/daemon/internal/protocol"
	"github.com/sirin-interact/worky/daemon/internal/watch"
)

const waitLimit = 20 * time.Second

// run runs git as a person with a name would.
func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	args = append([]string{
		"-c", "user.name=Test", "-c", "user.email=test@example.com",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main",
	}, args...)
	out, err := git(queryLimit, dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args[8:], " "), err)
	}
	return strings.TrimSpace(out)
}

// tempDir returns a new directory, spelled the way the system spells it.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// newRepo makes a repository with one commit on main, in a directory called
// name.
func newRepo(t *testing.T, name string) string {
	t.Helper()
	repo := filepath.Join(tempDir(t), name)
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "init")
	commit(t, repo, "README.md", "first")
	return repo
}

func commit(t *testing.T, repo, file, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, file), []byte(message), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "add", file)
	run(t, repo, "commit", "-m", message)
}

// recorder keeps the events a Manager sends.
type recorder struct {
	mu     sync.Mutex
	events []string
	last   map[string]protocol.Project
}

func (r *recorder) note(event string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	if result, ok := data.(protocol.ProjectResult); ok {
		if r.last == nil {
			r.last = make(map[string]protocol.Project)
		}
		r.last[result.Project.ID] = result.Project
	}
}

func (r *recorder) count(event string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e == event {
			n++
		}
	}
	return n
}

func (r *recorder) project(id string) protocol.Project {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last[id]
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// open returns a Manager that saves to a new directory. The directory is made
// before anything the test goes on to open inside its repositories.
func open(t *testing.T, watching bool) (*Manager, *recorder) {
	t.Helper()
	return openFile(t, filepath.Join(t.TempDir(), "projects.json"), watching)
}

func openFile(t *testing.T, file string, watching bool) (*Manager, *recorder) {
	t.Helper()
	var watcher *watch.Watcher
	if watching {
		var err error
		if watcher, err = watch.New(); err != nil {
			t.Fatal(err)
		}
	}
	m, err := Open(file, watcher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		m.Close()
		if watcher != nil {
			watcher.Close()
		}
	})
	r := &recorder{}
	m.Subscribe(r.note)
	return m, r
}

func branchOf(wt protocol.Worktree) string {
	if wt.Branch == nil {
		return "<none>"
	}
	return *wt.Branch
}

func describe(worktrees []protocol.Worktree) string {
	var parts []string
	for _, wt := range worktrees {
		parts = append(parts, filepath.Base(wt.Path)+"="+branchOf(wt))
	}
	return strings.Join(parts, " ")
}

func TestParseWorktrees(t *testing.T) {
	out := "worktree /src/app\nHEAD 1111\nbranch refs/heads/main\n\n" +
		"worktree /src/app.worktrees/fix\nHEAD 2222\nbranch refs/heads/team/fix\n\n" +
		"worktree /src/gone\nHEAD 3333\nbranch refs/heads/gone\nprunable gitdir file points to non-existent location\n\n" +
		"worktree /src/loose\nHEAD 4444\ndetached\nlocked\n\n"
	l := parseWorktrees(out, func(path string) bool { return filepath.Base(path) != "gone" })

	if l.main != filepath.FromSlash("/src/app") {
		t.Errorf("main = %q", l.main)
	}
	if got, want := describe(l.worktrees), "app=main fix=team/fix loose=<none>"; got != want {
		t.Errorf("worktrees = %s, want %s", got, want)
	}
	if !l.worktrees[0].Main || l.worktrees[1].Main || l.worktrees[2].Main {
		t.Errorf("main flags = %v", l.worktrees)
	}

	bare := parseWorktrees("worktree /srv/app.git\nbare\n\nworktree /src/work\nHEAD 1\nbranch refs/heads/main\n",
		func(string) bool { return true })
	if bare.main != filepath.FromSlash("/srv/app.git") || describe(bare.worktrees) != "work=main" || bare.worktrees[0].Main {
		t.Errorf("bare repository: main %q, worktrees %v", bare.main, bare.worktrees)
	}
}

func TestDirName(t *testing.T) {
	for branch, want := range map[string]string{
		"fix-login":     "fix-login",
		"team/fix":      "team-fix",
		`a\b:c*d?"e<f>`: "a-b-c-d--e-f",
		"한글/브랜치":        "한글-브랜치",
		"...":           "worktree",
	} {
		if got := dirName(branch); got != want {
			t.Errorf("dirName(%q) = %q, want %q", branch, got, want)
		}
	}
}

func TestAdd(t *testing.T) {
	m, events := open(t, false)
	repo := newRepo(t, "app")
	sub := filepath.Join(repo, "docs")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// Any directory in the repository names it.
	p, perr := m.Add(sub)
	if perr != nil {
		t.Fatal(perr)
	}
	if p.Name != "app" || !sameFile(p.Path, repo) || !strings.HasPrefix(p.ID, "p_") {
		t.Errorf("project = %+v", p)
	}
	if describe(p.Worktrees) != "app=main" || !p.Worktrees[0].Main || p.Worktrees[0].Path != p.Path {
		t.Errorf("worktrees = %+v", p.Worktrees)
	}

	again, perr := m.Add(repo)
	if perr != nil || again.ID != p.ID {
		t.Errorf("adding it again: %+v, %v", again, perr)
	}
	if n := events.count(protocol.EventProjectAdded); n != 1 {
		t.Errorf("%d added events, want 1", n)
	}
	if got := events.project(p.ID); describe(got.Worktrees) != "app=main" {
		t.Errorf("the added event carried %+v", got)
	}
	if list := m.List(); len(list) != 1 || list[0].ID != p.ID {
		t.Errorf("list = %+v", list)
	}

	for path, want := range map[string]protocol.ErrorCode{
		tempDir(t):                     protocol.ErrNotARepository,
		filepath.Join(repo, "missing"): protocol.ErrNotFound,
		"relative/path":                protocol.ErrInvalidRequest,
	} {
		if _, perr := m.Add(path); perr == nil || perr.Code != want {
			t.Errorf("Add(%q) = %v, want %s", path, perr, want)
		}
	}
}

func TestRenameAndRemove(t *testing.T) {
	m, events := open(t, false)
	p, perr := m.Add(newRepo(t, "app"))
	if perr != nil {
		t.Fatal(perr)
	}

	if perr := m.Rename(p.ID, "Shop"); perr != nil {
		t.Fatal(perr)
	}
	if got := events.project(p.ID); got.Name != "Shop" {
		t.Errorf("after the rename the event carried %+v", got)
	}
	if perr := m.Rename(p.ID, " "); perr == nil || perr.Code != protocol.ErrInvalidRequest {
		t.Errorf("an empty name: %v", perr)
	}
	if perr := m.Rename("p_none", "x"); perr == nil || perr.Code != protocol.ErrNotFound {
		t.Errorf("renaming an unknown project: %v", perr)
	}

	if perr := m.Remove(p.ID); perr != nil {
		t.Fatal(perr)
	}
	if len(m.List()) != 0 || events.count(protocol.EventProjectRemoved) != 1 {
		t.Errorf("after removing: list %+v", m.List())
	}
	if _, err := os.Stat(filepath.Join(p.Path, "README.md")); err != nil {
		t.Errorf("removing the project touched its files: %v", err)
	}
	if perr := m.Remove(p.ID); perr == nil || perr.Code != protocol.ErrNotFound {
		t.Errorf("removing it twice: %v", perr)
	}
}

func TestPersists(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state", "projects.json")
	repo := newRepo(t, "app")

	m, _ := openFile(t, file, false)
	p, perr := m.Add(repo)
	if perr != nil {
		t.Fatal(perr)
	}
	if perr := m.Rename(p.ID, "Shop"); perr != nil {
		t.Fatal(perr)
	}

	m2, _ := openFile(t, file, false)
	list := m2.List()
	if len(list) != 1 || list[0].ID != p.ID || list[0].Name != "Shop" || describe(list[0].Worktrees) != "app=main" {
		t.Fatalf("after a restart: %+v", list)
	}

	if perr := m2.Remove(p.ID); perr != nil {
		t.Fatal(perr)
	}
	m3, _ := openFile(t, file, false)
	if list := m3.List(); len(list) != 0 {
		t.Errorf("a removed project came back: %+v", list)
	}
}

func TestMissingRepositoryStaysListed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "projects.json")
	repo := newRepo(t, "app")
	m, _ := openFile(t, file, false)
	p, perr := m.Add(repo)
	if perr != nil {
		t.Fatal(perr)
	}
	m.Close()
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}

	m2, _ := openFile(t, file, false)
	list := m2.List()
	if len(list) != 1 || list[0].ID != p.ID || list[0].Worktrees == nil || len(list[0].Worktrees) != 0 {
		t.Errorf("a project whose repository is gone: %+v", list)
	}
}

func TestCreateAndRemoveWorktree(t *testing.T) {
	m, events := open(t, false)
	repo := newRepo(t, "app")
	p, perr := m.Add(repo)
	if perr != nil {
		t.Fatal(perr)
	}
	changes := 0
	m.OnWorktrees(func() { changes++ })

	// A new branch, in the place the daemon picks.
	wt, perr := m.CreateWorktree(protocol.WorktreeCreateParams{ProjectID: p.ID, Branch: "team/fix"})
	if perr != nil {
		t.Fatal(perr)
	}
	want := filepath.Join(p.Path+".worktrees", "team-fix")
	if !sameFile(wt.Path, want) || branchOf(wt) != "team/fix" || wt.Main {
		t.Errorf("worktree = %+v, want it at %s", wt, want)
	}
	if got := events.project(p.ID); describe(got.Worktrees) != "app=main team-fix=team/fix" {
		t.Errorf("the update carried %s", describe(got.Worktrees))
	}
	if changes == 0 {
		t.Error("nobody was told the worktrees changed")
	}
	if head := run(t, wt.Path, "rev-parse", "HEAD"); head != run(t, repo, "rev-parse", "HEAD") {
		t.Error("the new branch does not start where the main worktree is")
	}

	// A branch that exists, in a place the client picks.
	run(t, repo, "branch", "old")
	chosen := filepath.Join(tempDir(t), "elsewhere")
	old, perr := m.CreateWorktree(protocol.WorktreeCreateParams{ProjectID: p.ID, Branch: "old", Path: chosen})
	if perr != nil {
		t.Fatal(perr)
	}
	if !sameFile(old.Path, chosen) || branchOf(old) != "old" {
		t.Errorf("worktree = %+v", old)
	}

	// A new branch from a named commit.
	commit(t, repo, "second.txt", "second")
	first := run(t, repo, "rev-parse", "HEAD~1")
	based, perr := m.CreateWorktree(protocol.WorktreeCreateParams{ProjectID: p.ID, Branch: "based", Base: first})
	if perr != nil {
		t.Fatal(perr)
	}
	if head := run(t, based.Path, "rev-parse", "HEAD"); head != first {
		t.Errorf("the branch starts at %s, want %s", head, first)
	}

	for name, params := range map[string]protocol.WorktreeCreateParams{
		"a branch checked out elsewhere": {ProjectID: p.ID, Branch: "old"},
		"a base that is no commit":       {ProjectID: p.ID, Branch: "x", Base: "no-such-commit"},
	} {
		if _, perr := m.CreateWorktree(params); perr == nil || perr.Code != protocol.ErrGitFailed || perr.Message == "" {
			t.Errorf("%s: %v", name, perr)
		}
	}
	for name, params := range map[string]protocol.WorktreeCreateParams{
		"no branch":             {ProjectID: p.ID},
		"an option as a branch": {ProjectID: p.ID, Branch: "--force"},
		"a bad branch name":     {ProjectID: p.ID, Branch: "a..b"},
		"an option as a base":   {ProjectID: p.ID, Branch: "y", Base: "--detach"},
		"a relative path":       {ProjectID: p.ID, Branch: "z", Path: "rel"},
	} {
		if _, perr := m.CreateWorktree(params); perr == nil || perr.Code != protocol.ErrInvalidRequest {
			t.Errorf("%s: %v", name, perr)
		}
	}
	if _, perr := m.CreateWorktree(protocol.WorktreeCreateParams{ProjectID: p.ID, Branch: "w", Path: repo}); perr == nil || perr.Code != protocol.ErrAlreadyExists {
		t.Errorf("a path that is taken: %v", perr)
	}
	if _, perr := m.CreateWorktree(protocol.WorktreeCreateParams{ProjectID: "p_none", Branch: "w"}); perr == nil || perr.Code != protocol.ErrNotFound {
		t.Errorf("an unknown project: %v", perr)
	}
	if _, err := os.Stat(filepath.Join(p.Path+".worktrees", "x")); err == nil {
		t.Error("a failed create left its directory")
	}

	// Removing.
	remove := func(path string, force bool) *protocol.Error {
		return m.RemoveWorktree(protocol.WorktreeRemoveParams{ProjectID: p.ID, Path: path, Force: force})
	}
	if perr := remove(p.Path, true); perr == nil || perr.Code != protocol.ErrInvalidRequest {
		t.Errorf("removing the main worktree: %v", perr)
	}
	if perr := remove(filepath.Join(p.Path, "nowhere"), false); perr == nil || perr.Code != protocol.ErrNotFound {
		t.Errorf("removing a worktree that is not one: %v", perr)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if perr := remove(wt.Path, false); perr == nil || perr.Code != protocol.ErrGitFailed {
		t.Errorf("removing a worktree with changes: %v", perr)
	}
	if perr := remove(wt.Path, true); perr != nil {
		t.Fatalf("removing it with force: %v", perr)
	}
	if _, err := os.Stat(wt.Path); err == nil {
		t.Error("the directory is still there")
	}
	if !succeeds(repo, "show-ref", "--verify", "--quiet", "refs/heads/team/fix") {
		t.Error("the branch went with the worktree")
	}
	if perr := remove(based.Path, false); perr != nil {
		t.Fatal(perr)
	}
	if _, err := os.Stat(p.Path + ".worktrees"); err == nil {
		t.Error("the empty directory for worktrees is still there")
	}
	if perr := remove(old.Path, false); perr != nil {
		t.Fatal(perr)
	}
	if got := events.project(p.ID); describe(got.Worktrees) != "app=main" {
		t.Errorf("at the end the project has %s", describe(got.Worktrees))
	}
}

func TestBranchFromRemote(t *testing.T) {
	m, _ := open(t, false)
	origin := newRepo(t, "origin")
	run(t, origin, "branch", "topic")
	clone := filepath.Join(tempDir(t), "clone")
	run(t, filepath.Dir(clone), "clone", origin, clone)

	p, perr := m.Add(clone)
	if perr != nil {
		t.Fatal(perr)
	}
	upstream := func(branch string) string {
		out, err := git(queryLimit, clone, "rev-parse", "--abbrev-ref", branch+"@{upstream}")
		if err != nil {
			return ""
		}
		return strings.TrimSpace(out)
	}

	// The clone has no branch called topic, and one remote has.
	if _, perr := m.CreateWorktree(protocol.WorktreeCreateParams{ProjectID: p.ID, Branch: "topic"}); perr != nil {
		t.Fatal(perr)
	}
	if got := upstream("topic"); got != "origin/topic" {
		t.Errorf("topic follows %q, want origin/topic", got)
	}

	// A new branch does not follow the remote branch it starts from.
	if _, perr := m.CreateWorktree(protocol.WorktreeCreateParams{ProjectID: p.ID, Branch: "mine", Base: "origin/main"}); perr != nil {
		t.Fatal(perr)
	}
	if got := upstream("mine"); got != "" {
		t.Errorf("mine follows %q, want nothing", got)
	}
}

func TestNoticesChangesMadeElsewhere(t *testing.T) {
	m, events := open(t, true)
	repo := newRepo(t, "app")
	p, perr := m.Add(repo)
	if perr != nil {
		t.Fatal(perr)
	}
	shows := func(want string) func() bool {
		return func() bool { return describe(events.project(p.ID).Worktrees) == want }
	}

	other := filepath.Join(tempDir(t), "other")
	run(t, repo, "worktree", "add", "-b", "side", other)
	eventually(t, "the new worktree", shows("app=main other=side"))

	run(t, other, "checkout", "-b", "side-2")
	eventually(t, "the branch switch in the linked worktree", shows("app=main other=side-2"))

	run(t, repo, "checkout", "--detach")
	eventually(t, "the detached main worktree", shows("app=<none> other=side-2"))

	run(t, repo, "checkout", "main")
	run(t, repo, "worktree", "remove", other)
	eventually(t, "the removed worktree", shows("app=main"))

	// A second one, after the directory for linked worktrees came and went.
	run(t, repo, "worktree", "add", "-b", "late", other)
	eventually(t, "a worktree added later", shows("app=main other=late"))
}

func TestWorktreeOf(t *testing.T) {
	m, _ := open(t, false)
	repo := newRepo(t, "app")
	p, perr := m.Add(repo)
	if perr != nil {
		t.Fatal(perr)
	}
	// A worktree inside another, as some agents make them.
	inner, perr := m.CreateWorktree(protocol.WorktreeCreateParams{
		ProjectID: p.ID, Branch: "inner", Path: filepath.Join(repo, ".trees", "inner"),
	})
	if perr != nil {
		t.Fatal(perr)
	}
	deep := filepath.Join(inner.Path, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		repo:                                p.Path,
		filepath.Join(repo, ".trees"):       p.Path,
		inner.Path:                          inner.Path,
		deep:                                inner.Path,
		filepath.Join(deep, "not", "there"): inner.Path,
		filepath.Dir(repo):                  "",
		tempDir(t):                          "",
	}
	if runtime.GOOS == "windows" {
		cases[strings.ToUpper(deep)] = inner.Path
		cases[filepath.ToSlash(deep)] = inner.Path
	}
	for path, want := range cases {
		if got := m.WorktreeOf(path); got != want {
			t.Errorf("WorktreeOf(%q) = %q, want %q", path, got, want)
		}
	}

	if perr := m.Remove(p.ID); perr != nil {
		t.Fatal(perr)
	}
	if got := m.WorktreeOf(deep); got != "" {
		t.Errorf("after the project is removed, WorktreeOf = %q", got)
	}
}
