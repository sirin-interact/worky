// Package watch reports changes to the entries of directories.
//
// A watch covers the entries directly inside one directory. Changes are
// collected for a moment and reported together, by name. What happened to a
// name is not reported, because the operating systems disagree about it.
package watch

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// How long changes to a directory are collected before they are reported.
	defaultDelay = 100 * time.Millisecond

	// A report with more names than this says only that the directory changed.
	maxNames = 256

	// How often every watched directory is looked at. Nothing reports a
	// directory that moved because one above it was renamed, unless that
	// one's parent is watched too. And some file systems, those reached over
	// a network among them, report nothing at all.
	defaultCheck = 5 * time.Second
)

// ErrNotDir is returned when the path to watch is not a directory.
var ErrNotDir = errors.New("not a directory")

// Change is what happened in a watched directory since the last report.
type Change struct {
	// Names are the entries that were created, deleted, renamed or written
	// to, sorted. Nil means too much happened to tell.
	Names []string
	// Ended means the directory itself is gone and the watch is over.
	Ended bool
}

// backend is what the operating system offers. Its methods are never called
// from inside the sink, which a backend calls from its own goroutine.
type backend interface {
	// add starts reporting changes to the entries of dir. root, when set, is
	// a directory above dir that the backend may watch instead of dir itself.
	add(dir, root string) error
	// remove undoes add. It is harmless for a directory that is not watched.
	remove(dir string)
	close() error
}

// sink is where a backend reports what it saw.
type sink interface {
	// changed says the entry at path was created, deleted, renamed or
	// written to.
	changed(path string)
	// gone says whatever was at path was deleted or moved. If it was a
	// directory, that goes for everything below it too.
	gone(path string)
	// lost says changes below root were missed. An empty root means anywhere.
	lost(root string)
}

// Watcher watches directories.
type Watcher struct {
	delay   time.Duration
	backend backend

	// addMu is held while the backend is told to add or remove a directory.
	// It is never taken by the backend's own goroutine.
	addMu sync.Mutex

	mu     sync.Mutex
	dirs   map[string]*dir
	closed bool

	stop chan struct{}
}

// dir is one watched directory and everyone watching it.
type dir struct {
	key  string
	path string
	info os.FileInfo // tells the directory from one that takes its place
	subs map[*Watch]struct{}

	// For the periodic look: when the directory's entries last changed, and
	// whether the backend has reported anything since the look before.
	mtime    time.Time
	reported bool

	// What to report next.
	names   map[string]struct{}
	lost    bool
	ended   bool
	pending bool // a report is scheduled
}

// Watch is one caller's interest in a directory.
type Watch struct {
	w  *Watcher
	d  *dir
	fn func(Change)
}

// New returns a Watcher.
func New() (*Watcher, error) { return newWatcher(defaultCheck, newBackend) }

func newWatcher(check time.Duration, open func(sink) (backend, error)) (*Watcher, error) {
	w := &Watcher{delay: defaultDelay, dirs: make(map[string]*dir), stop: make(chan struct{})}
	b, err := open(w)
	if err != nil {
		return nil, err
	}
	w.backend = b
	go w.checkLoop(check)
	return w, nil
}

// Close stops every watch.
func (w *Watcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.dirs = make(map[string]*dir)
	w.mu.Unlock()
	close(w.stop)
	return w.backend.close()
}

// checkLoop ends the watches whose directory is no longer at its path, and
// reports the directories whose entries changed without the backend saying
// so.
func (w *Watcher) checkLoop(every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
		}
		// What was reported up to now is noted before the directories are
		// looked at. A report that comes in during the look then counts for
		// this look and for the next one.
		w.mu.Lock()
		dirs := make([]*dir, 0, len(w.dirs))
		told := make([]bool, 0, len(w.dirs))
		for _, d := range w.dirs {
			dirs, told = append(dirs, d), append(told, d.reported)
			d.reported = false
		}
		w.mu.Unlock()
		for i, d := range dirs {
			info, err := os.Stat(d.path)
			if err != nil || !os.SameFile(info, d.info) {
				w.gone(d.path)
				continue
			}
			w.mu.Lock()
			if w.dirs[d.key] == d {
				// A directory's time moves when an entry is added, deleted
				// or renamed.
				if !info.ModTime().Equal(d.mtime) && !told[i] && !d.reported {
					d.lost = true
					w.schedule(d)
				}
				d.mtime = info.ModTime()
			}
			w.mu.Unlock()
		}
	}
}

// key is what a directory is looked up by. Names differ only in case on
// Windows.
func key(path string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

// under returns what the key of everything below the directory with key k
// starts with.
func under(k string) string {
	return strings.TrimSuffix(k, string(filepath.Separator)) + string(filepath.Separator)
}

// Add watches the directory at path and calls fn with what changes in it. fn
// is called from another goroutine and must not block. It can be called once
// more after Stop.
//
// root, when set, names a directory above path that is safe to keep open. On
// Windows a directory that is open cannot have a directory above it renamed,
// so the watcher opens root instead of path where it can.
func (w *Watcher) Add(path, root string, fn func(Change)) (*Watch, error) {
	// Two spellings of one directory share a watch, and the names the
	// backend reports are below the real one.
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, &os.PathError{Op: "watch", Path: path, Err: ErrNotDir}
	}
	if root != "" {
		if root, err = filepath.EvalSymlinks(root); err != nil {
			root = ""
		}
	}

	w.addMu.Lock()
	defer w.addMu.Unlock()

	x := &Watch{w: w, fn: fn}
	k := key(real)

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil, os.ErrClosed
	}
	if d := w.dirs[k]; d != nil {
		x.d = d
		d.subs[x] = struct{}{}
		w.mu.Unlock()
		return x, nil
	}
	w.mu.Unlock()

	if err := w.backend.add(real, root); err != nil {
		return nil, err
	}

	d := &dir{
		key: k, path: real, info: info, mtime: info.ModTime(),
		subs: map[*Watch]struct{}{x: {}}, names: make(map[string]struct{}),
	}
	x.d = d
	w.mu.Lock()
	w.dirs[k] = d
	w.mu.Unlock()
	return x, nil
}

// Stop ends the watch.
func (x *Watch) Stop() {
	w := x.w
	w.addMu.Lock()
	defer w.addMu.Unlock()

	w.mu.Lock()
	delete(x.d.subs, x)
	last := len(x.d.subs) == 0 && w.dirs[x.d.key] == x.d
	if last {
		delete(w.dirs, x.d.key)
	}
	w.mu.Unlock()

	if last {
		w.backend.remove(x.d.path)
	}
}

// schedule arranges for d to be reported. The caller holds w.mu.
func (w *Watcher) schedule(d *dir) {
	if d.pending {
		return
	}
	d.pending = true
	time.AfterFunc(w.delay, func() { w.report(d) })
}

func (w *Watcher) report(d *dir) {
	w.mu.Lock()
	change := Change{Ended: d.ended}
	if !d.lost && len(d.names) <= maxNames {
		change.Names = make([]string, 0, len(d.names))
		for name := range d.names {
			change.Names = append(change.Names, name)
		}
		sort.Strings(change.Names)
	}
	empty := !d.lost && !d.ended && len(d.names) == 0
	d.names, d.lost, d.pending = make(map[string]struct{}), false, false
	subs := make([]*Watch, 0, len(d.subs))
	for x := range d.subs {
		subs = append(subs, x)
	}
	w.mu.Unlock()

	if empty {
		return
	}
	for _, x := range subs {
		x.fn(change)
	}
}

func (w *Watcher) changed(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if d := w.dirs[key(filepath.Dir(path))]; d != nil {
		d.names[filepath.Base(path)] = struct{}{}
		d.reported = true
		w.schedule(d)
	}
}

func (w *Watcher) gone(path string) {
	k := key(path)
	below := under(k)

	w.mu.Lock()
	var ended []*dir
	for dk, d := range w.dirs {
		if dk == k || strings.HasPrefix(dk, below) {
			d.ended = true
			delete(w.dirs, dk)
			w.schedule(d)
			ended = append(ended, d)
		}
	}
	w.mu.Unlock()
	if len(ended) == 0 {
		return
	}

	// The backend may still hold the directories. It cannot be told from
	// here, because this can run in its goroutine.
	go func() {
		w.addMu.Lock()
		defer w.addMu.Unlock()
		for _, d := range ended {
			w.mu.Lock()
			again := w.dirs[d.key] != nil // watched again in the meantime
			w.mu.Unlock()
			if !again {
				w.backend.remove(d.path)
			}
		}
	}()
}

func (w *Watcher) lost(root string) {
	prefix := key(root)
	w.mu.Lock()
	defer w.mu.Unlock()
	for k, d := range w.dirs {
		if prefix == "" || k == prefix || strings.HasPrefix(k, under(prefix)) {
			d.lost = true
			d.reported = true
			w.schedule(d)
		}
	}
}
