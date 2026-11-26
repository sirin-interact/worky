// Package project keeps the git repositories the daemon has been told about,
// and their worktrees.
package project

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirin-interact/worky/daemon/internal/protocol"
	"github.com/sirin-interact/worky/daemon/internal/watch"
)

// How often each worktree's directory is checked to still be there. Deleting
// one by hand changes nothing git keeps, so nothing else would say. Tests
// lower it.
var checkInterval = 5 * time.Second

const (
	// A repository that could not be read is tried again every so many
	// checks.
	retryEvery = 6

	// The least time between two readings of a repository's worktrees that
	// changes on disk set off.
	refreshGap = 500 * time.Millisecond
)

// Manager owns the daemon's projects.
type Manager struct {
	file    string
	watcher *watch.Watcher

	// addMu is held while a project is added, so one repository is not added
	// twice.
	addMu sync.Mutex

	// emitMu is held while an event is sent, so subscribers get events in the
	// order things happened.
	emitMu sync.Mutex

	mu          sync.Mutex
	projects    []*project
	subs        map[uint64]func(event string, data any)
	nextSub     uint64
	onWorktrees func()

	stop     chan struct{}
	stopOnce sync.Once
}

type project struct {
	id   string
	path string

	// Guarded by Manager.mu.
	name      string
	worktrees []protocol.Worktree
	roots     []root

	// refreshMu is held while the worktrees are read again.
	refreshMu sync.Mutex
	queued    atomic.Bool
	refreshed atomic.Int64 // when the last reading began, in unix nanoseconds
	// The rest is guarded by refreshMu.
	readable bool
	common   string // the repository's own directory, once known
	watches  map[string]*watch.Watch
	removed  bool
}

// root is a worktree's directory, and what tells it from any other.
type root struct {
	path string
	info os.FileInfo
}

// stored is the part of a project that is kept between runs.
type stored struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type storedFile struct {
	Projects []stored `json:"projects"`
}

// Open returns a Manager holding the projects saved in file. watcher lets it
// notice what git commands run elsewhere change. It may be nil.
func Open(file string, watcher *watch.Watcher) (*Manager, error) {
	m := &Manager{
		file:    file,
		watcher: watcher,
		subs:    make(map[uint64]func(string, any)),
		stop:    make(chan struct{}),
	}

	data, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	var saved storedFile
	if len(data) > 0 {
		if err := json.Unmarshal(data, &saved); err != nil {
			return nil, &os.PathError{Op: "read", Path: file, Err: err}
		}
	}

	var wg sync.WaitGroup
	for _, s := range saved.Projects {
		p := &project{id: s.ID, name: s.Name, path: s.Path}
		m.projects = append(m.projects, p)
		wg.Go(func() { m.refresh(p) })
	}
	wg.Wait()

	go m.checkLoop(checkInterval)
	return m, nil
}

// Close stops the Manager's background work.
func (m *Manager) Close() {
	m.stopOnce.Do(func() { close(m.stop) })
	m.mu.Lock()
	projects := slices.Clone(m.projects)
	m.mu.Unlock()
	for _, p := range projects {
		p.refreshMu.Lock()
		p.removed = true
		m.syncWatches(p, nil)
		p.refreshMu.Unlock()
	}
}

// OnWorktrees sets a function to call whenever the set of worktrees may have
// changed. fn must not call back into the Manager's changing methods.
func (m *Manager) OnWorktrees(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onWorktrees = fn
}

func (m *Manager) worktreesChanged() {
	m.mu.Lock()
	fn := m.onWorktrees
	m.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Subscribe calls fn for every project event until the returned function is
// called. fn must not block.
func (m *Manager) Subscribe(fn func(event string, data any)) (unsubscribe func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextSub
	m.nextSub++
	m.subs[id] = fn
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.subs, id)
	}
}

// deliver sends an event to every subscriber. The caller holds emitMu.
func (m *Manager) deliver(event string, data any) {
	m.mu.Lock()
	subs := make([]func(string, any), 0, len(m.subs))
	for _, fn := range m.subs {
		subs = append(subs, fn)
	}
	m.mu.Unlock()
	for _, fn := range subs {
		fn(event, data)
	}
}

// emitUpdated tells subscribers what p looks like now, unless it is no
// longer listed.
func (m *Manager) emitUpdated(p *project) {
	m.emitMu.Lock()
	defer m.emitMu.Unlock()

	m.mu.Lock()
	listed := slices.Contains(m.projects, p)
	info := m.infoLocked(p)
	m.mu.Unlock()
	if listed {
		m.deliver(protocol.EventProjectUpdated, protocol.ProjectResult{Project: info})
	}
}

// infoLocked returns a snapshot of p. The caller holds m.mu.
func (m *Manager) infoLocked(p *project) protocol.Project {
	worktrees := slices.Clone(p.worktrees)
	if worktrees == nil {
		worktrees = []protocol.Worktree{}
	}
	return protocol.Project{ID: p.id, Name: p.name, Path: p.path, Worktrees: worktrees}
}

// List returns every project, in the order they were added.
func (m *Manager) List() []protocol.Project {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]protocol.Project, len(m.projects))
	for i, p := range m.projects {
		out[i] = m.infoLocked(p)
	}
	return out
}

func (m *Manager) find(id string) (*project, *protocol.Error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.projects {
		if p.id == id {
			return p, nil
		}
	}
	return nil, protocol.Errorf(protocol.ErrNotFound, "no project %q", id)
}

// save writes the list of projects to disk. The caller holds m.mu.
func (m *Manager) saveLocked() {
	var out storedFile
	for _, p := range m.projects {
		out.Projects = append(out.Projects, stored{ID: p.id, Name: p.name, Path: p.path})
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err == nil {
		err = os.MkdirAll(filepath.Dir(m.file), 0o700)
	}
	if err == nil {
		// Written beside the file and moved over it, so a crash leaves the
		// old list rather than half of the new one.
		tmp := m.file + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, m.file)
		}
	}
	if err != nil {
		slog.Error("saving projects", "err", err)
	}
}

func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

func absolute(path string) (string, *protocol.Error) {
	if !filepath.IsAbs(path) {
		return "", protocol.Errorf(protocol.ErrInvalidRequest, "path %q is not absolute", path)
	}
	return filepath.Clean(path), nil
}

// Add registers the repository that path is in.
func (m *Manager) Add(path string) (protocol.Project, *protocol.Error) {
	path, perr := absolute(path)
	if perr != nil {
		return protocol.Project{}, perr
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return protocol.Project{}, protocol.Errorf(protocol.ErrNotFound, "%q is not a directory", path)
	}
	l, err := listWorktrees(path)
	if err != nil {
		return protocol.Project{}, protocolError(err)
	}
	if l.main == "" {
		return protocol.Project{}, protocol.Errorf(protocol.ErrGitFailed, "git listed no worktrees")
	}

	m.addMu.Lock()
	defer m.addMu.Unlock()

	m.mu.Lock()
	projects := slices.Clone(m.projects)
	m.mu.Unlock()
	var known *project
	for _, p := range projects {
		if p.path == l.main || sameFile(p.path, l.main) {
			known = p
		}
	}
	if known != nil {
		m.refresh(known)
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.infoLocked(known), nil
	}

	p := &project{
		id:   newID(),
		name: strings.TrimSuffix(filepath.Base(l.main), ".git"),
		path: l.main,
	}
	if p.name == "" || p.name == string(filepath.Separator) {
		p.name = l.main
	}
	// Read before it is listed, so the added event already has the
	// worktrees.
	m.refresh(p)

	m.emitMu.Lock()
	m.mu.Lock()
	m.projects = append(m.projects, p)
	m.saveLocked()
	info := m.infoLocked(p)
	m.mu.Unlock()
	m.deliver(protocol.EventProjectAdded, protocol.ProjectResult{Project: info})
	m.emitMu.Unlock()

	m.worktreesChanged()
	return info, nil
}

// Rename changes a project's name.
func (m *Manager) Rename(id, name string) *protocol.Error {
	if strings.TrimSpace(name) == "" {
		return protocol.Errorf(protocol.ErrInvalidRequest, "a project needs a name")
	}
	p, perr := m.find(id)
	if perr != nil {
		return perr
	}
	m.mu.Lock()
	changed := p.name != name
	if changed {
		p.name = name
		m.saveLocked()
	}
	m.mu.Unlock()
	if changed {
		m.emitUpdated(p)
	}
	return nil
}

// Remove forgets a project. Nothing on disk changes.
func (m *Manager) Remove(id string) *protocol.Error {
	m.mu.Lock()
	var p *project
	for i, other := range m.projects {
		if other.id == id {
			p = other
			m.projects = slices.Delete(m.projects, i, i+1)
			m.saveLocked()
			break
		}
	}
	m.mu.Unlock()
	if p == nil {
		return protocol.Errorf(protocol.ErrNotFound, "no project %q", id)
	}

	p.refreshMu.Lock()
	p.removed = true
	m.syncWatches(p, nil)
	p.refreshMu.Unlock()

	m.emitMu.Lock()
	m.deliver(protocol.EventProjectRemoved, protocol.ProjectIDParams{ProjectID: id})
	m.emitMu.Unlock()

	m.worktreesChanged()
	return nil
}

// refresh reads p's worktrees again and tells subscribers if they changed.
func (m *Manager) refresh(p *project) {
	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()
	if p.removed {
		return
	}

	p.refreshed.Store(time.Now().UnixNano())

	var l listing
	var err error
	for attempt := 0; ; attempt++ {
		if l, err = listWorktrees(p.path); err != nil {
			slog.Warn("reading worktrees", "project", p.path, "err", err)
			break
		}
		if p.common == "" {
			p.common, _ = commonDir(p.path)
		}
		// What changed in a directory before it was watched was not
		// reported, so a listing only counts once everything it shows was
		// being watched when it was made.
		if added := m.syncWatches(p, gitDirs(p.common)); !added || attempt == 2 {
			break
		}
	}
	p.readable = err == nil
	roots := make([]root, 0, len(l.worktrees))
	for _, wt := range l.worktrees {
		if info, err := os.Stat(wt.Path); err == nil {
			roots = append(roots, root{path: wt.Path, info: info})
		}
	}

	m.mu.Lock()
	changed := !slices.EqualFunc(p.worktrees, l.worktrees, func(a, b protocol.Worktree) bool {
		return a.Path == b.Path && a.Main == b.Main &&
			(a.Branch == nil) == (b.Branch == nil) && (a.Branch == nil || *a.Branch == *b.Branch)
	})
	p.worktrees, p.roots = l.worktrees, roots
	m.mu.Unlock()

	if changed {
		m.emitUpdated(p)
		m.worktreesChanged()
	}
}

// refreshSoon reads p's worktrees again in the background. Calls that come
// while one is waiting to start are folded into it, and it waits out
// refreshGap, so a burst of changes does not run git over and over.
func (m *Manager) refreshSoon(p *project) {
	if p.queued.CompareAndSwap(false, true) {
		go func() {
			if wait := time.Until(time.Unix(0, p.refreshed.Load()).Add(refreshGap)); wait > 0 {
				time.Sleep(wait)
			}
			p.queued.Store(false)
			m.refresh(p)
		}()
	}
}

// checkLoop reads a repository again when the directory of one of its
// worktrees is gone, and now and then when it could not be read at all, as
// with one on a drive that was not there yet when the daemon started.
func (m *Manager) checkLoop(every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for tick := 1; ; tick++ {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
		}
		m.mu.Lock()
		projects := slices.Clone(m.projects)
		roots := make([][]root, len(projects))
		for i, p := range projects {
			roots[i] = p.roots
		}
		m.mu.Unlock()

		for i, p := range projects {
			p.refreshMu.Lock()
			stale := !p.readable && tick%retryEvery == 0
			p.refreshMu.Unlock()
			for _, r := range roots[i] {
				if info, err := os.Stat(r.path); err != nil || !os.SameFile(info, r.info) {
					stale = true
				}
			}
			if stale {
				m.refresh(p)
			}
		}
	}
}

// gitDirs returns the directories whose entries say which worktrees a
// repository has and what each has checked out, with the entries that matter
// in each. A nil list of entries means all of them. common is the
// repository's own directory.
func gitDirs(common string) map[string][]string {
	if common == "" {
		return nil
	}
	dirs := map[string][]string{common: {"HEAD", "worktrees"}}
	linked := filepath.Join(common, "worktrees")
	entries, err := os.ReadDir(linked)
	if err != nil {
		return dirs
	}
	dirs[linked] = nil
	for _, entry := range entries {
		if entry.IsDir() {
			dirs[filepath.Join(linked, entry.Name())] = []string{"HEAD", "gitdir"}
		}
	}
	return dirs
}

// syncWatches makes the directories watched for p exactly dirs, and reports
// whether it began to watch any. The caller holds p.refreshMu.
func (m *Manager) syncWatches(p *project, dirs map[string][]string) (added bool) {
	if m.watcher == nil {
		return false
	}
	for dir, x := range p.watches {
		if _, ok := dirs[dir]; !ok {
			x.Stop()
			delete(p.watches, dir)
		}
	}
	// On Windows an open directory keeps every directory above it from
	// being renamed. Watching through the main worktree leaves that one
	// free to be renamed, where opening .git inside it would not.
	root := p.path
	if rel, err := filepath.Rel(p.path, p.common); err != nil || strings.HasPrefix(rel, "..") {
		root = p.common
	}
	for dir, names := range dirs {
		if _, ok := p.watches[dir]; ok {
			continue
		}
		var x *watch.Watch
		x, err := m.watcher.Add(dir, root, func(c watch.Change) {
			matters := c.Names == nil || names == nil
			for _, name := range c.Names {
				matters = matters || slices.Contains(names, name)
			}
			// A watch that ended is replaced by the refresh.
			if c.Ended {
				p.refreshMu.Lock()
				if p.watches[dir] == x {
					delete(p.watches, dir)
				}
				p.refreshMu.Unlock()
				matters = true
			}
			if matters {
				m.refreshSoon(p)
			}
		})
		if err != nil {
			continue
		}
		if p.watches == nil {
			p.watches = make(map[string]*watch.Watch)
		}
		p.watches[dir] = x
		added = true
	}
	return added
}

// WorktreeOf returns the path of the worktree that path is in, spelled as the
// worktree's entry has it, or "" when it is in none. The innermost one wins
// when worktrees are nested.
func (m *Manager) WorktreeOf(path string) string {
	m.mu.Lock()
	var roots []root
	for _, p := range m.projects {
		roots = append(roots, p.roots...)
	}
	m.mu.Unlock()
	if len(roots) == 0 {
		return ""
	}

	// Compared as files rather than as text: case, links and short names
	// give one directory many spellings.
	for dir := filepath.Clean(path); ; {
		if info, err := os.Stat(dir); err == nil {
			for _, r := range roots {
				if os.SameFile(info, r.info) {
					return r.path
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// CreateWorktree adds a worktree with a branch checked out.
func (m *Manager) CreateWorktree(params protocol.WorktreeCreateParams) (protocol.Worktree, *protocol.Error) {
	fail := func(perr *protocol.Error) (protocol.Worktree, *protocol.Error) {
		return protocol.Worktree{}, perr
	}
	p, perr := m.find(params.ProjectID)
	if perr != nil {
		return fail(perr)
	}
	branch, base := params.Branch, params.Base
	// A leading dash would be read as an option.
	if branch == "" || strings.HasPrefix(branch, "-") || !succeeds(p.path, "check-ref-format", "--branch", branch) {
		return fail(protocol.Errorf(protocol.ErrInvalidRequest, "%q is not a valid branch name", branch))
	}
	if strings.HasPrefix(base, "-") {
		return fail(protocol.Errorf(protocol.ErrInvalidRequest, "%q does not name a commit", base))
	}

	var target string
	if params.Path != "" {
		if target, perr = absolute(params.Path); perr != nil {
			return fail(perr)
		}
		if !free(target) {
			return fail(protocol.Errorf(protocol.ErrAlreadyExists, "%q is taken", target))
		}
	} else {
		// Two branches can come out as the same directory name, a/b and a-b
		// for one. The later one gets a number.
		name := dirName(branch)
		target = filepath.Join(p.path+".worktrees", name)
		for n := 2; !free(target); n++ {
			if n > 100 {
				return fail(protocol.Errorf(protocol.ErrAlreadyExists, "%q is taken", target))
			}
			target = filepath.Join(p.path+".worktrees", name+"-"+strconv.Itoa(n))
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
		return fail(protocol.Errorf(protocol.ErrIO, "%v", err))
	}
	// A worktree whose directory was deleted by hand is still on git's
	// books, and git will not put the branch or the directory to use again
	// until it is struck off.
	if registeredButGone(p.path, target, branch) {
		if _, err := git(queryLimit, p.path, "worktree", "prune"); err != nil {
			slog.Warn("pruning worktrees", "project", p.path, "err", err)
		}
	}

	args := []string{"worktree", "add"}
	switch {
	case succeeds(p.path, "show-ref", "--verify", "--quiet", "refs/heads/"+branch):
		args = append(args, target, branch)
	case base != "":
		// A new branch does not follow the one it starts from.
		args = append(args, "--no-track", "-b", branch, target, base)
	default:
		if remote := remoteBranch(p.path, branch); remote != "" {
			args = append(args, "--track", "-b", branch, target, remote)
		} else {
			args = append(args, "-b", branch, target)
		}
	}
	if _, err := git(checkoutLimit, p.path, args...); err != nil {
		if params.Path == "" {
			_ = os.Remove(filepath.Dir(target)) // only goes if it is empty
		}
		return fail(protocolError(err))
	}

	m.refresh(p)
	if wt, ok := m.worktreeAt(p, target); ok {
		return wt, nil
	}
	return protocol.Worktree{Path: target, Branch: &branch}, nil
}

// worktreeAt returns p's worktree at path.
func (m *Manager) worktreeAt(p *project, path string) (protocol.Worktree, bool) {
	m.mu.Lock()
	worktrees := slices.Clone(p.worktrees)
	m.mu.Unlock()
	for _, wt := range worktrees {
		if wt.Path == path || sameFile(wt.Path, path) {
			return wt, true
		}
	}
	return protocol.Worktree{}, false
}

// free reports whether a worktree can be put at path: nothing is there, or
// an empty directory.
func free(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	if !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) == 0
}

// registeredButGone reports whether git still lists a worktree at target, or
// one with branch checked out, whose directory no longer exists.
func registeredButGone(repo, target, branch string) bool {
	out, err := git(queryLimit, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	all := parseWorktrees(out, func(string) bool { return true })
	for _, wt := range all.worktrees {
		if _, err := os.Stat(wt.Path); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		same := wt.Path == target || (runtime.GOOS == "windows" && strings.EqualFold(wt.Path, target))
		if same || (wt.Branch != nil && *wt.Branch == branch) {
			return true
		}
	}
	return false
}

// remoteBranch returns the one remote branch called branch, such as
// origin/fix, or "" when there is none or more than one.
func remoteBranch(repo, branch string) string {
	out, err := git(queryLimit, repo, "for-each-ref", "--format=%(refname)", "refs/remotes/*/"+branch)
	if err != nil {
		return ""
	}
	refs := strings.Fields(out)
	if len(refs) != 1 {
		return ""
	}
	return strings.TrimPrefix(refs[0], "refs/remotes/")
}

// FindWorktree returns the entry of a project's worktree at path.
func (m *Manager) FindWorktree(projectID, path string) (protocol.Worktree, *protocol.Error) {
	p, perr := m.find(projectID)
	if perr != nil {
		return protocol.Worktree{}, perr
	}
	path, perr = absolute(path)
	if perr != nil {
		return protocol.Worktree{}, perr
	}
	if wt, ok := m.worktreeAt(p, path); ok {
		return wt, nil
	}
	return protocol.Worktree{}, protocol.Errorf(protocol.ErrNotFound, "project has no worktree at %q", path)
}

// RemoveWorktree deletes a worktree's directory and git's record of it. The
// branch stays.
func (m *Manager) RemoveWorktree(params protocol.WorktreeRemoveParams) *protocol.Error {
	p, perr := m.find(params.ProjectID)
	if perr != nil {
		return perr
	}
	wt, perr := m.FindWorktree(params.ProjectID, params.Path)
	if perr != nil {
		return perr
	}
	if wt.Main {
		return protocol.Errorf(protocol.ErrInvalidRequest, "the main worktree cannot be removed")
	}

	args := []string{"worktree", "remove"}
	if params.Force {
		args = append(args, "--force")
	}
	_, err := git(checkoutLimit, p.path, append(args, wt.Path)...)
	m.refresh(p)
	if err != nil {
		return protocolError(err)
	}
	if parent := filepath.Dir(wt.Path); parent == p.path+".worktrees" {
		_ = os.Remove(parent) // only goes if that was the last one in it
	}
	return nil
}

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // the system's random source is broken
	}
	return "p_" + hex.EncodeToString(b[:])
}
