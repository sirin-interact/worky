//go:build !windows

package watch

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	// How long after a watched directory shows up as new its watch is asked
	// for again, and how often every watch is.
	//
	// On kqueue, fsnotify watches the entries of a watched directory for
	// deletion on its own. When it does that for a directory which is itself
	// being added at that moment, the two registrations can cross, and the
	// directory is left watched for deletion only: nothing in it is reported
	// any more. Asking again puts the registration right.
	againSoon  = 50 * time.Millisecond
	againEvery = 5 * time.Second
)

// notifyBackend watches each directory on its own, through fsnotify.
type notifyBackend struct {
	watcher *fsnotify.Watcher
	sink    sink
	stop    chan struct{}

	mu   sync.Mutex
	dirs map[string]struct{}
}

func newNotifyBackend(s sink) (backend, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	b := &notifyBackend{watcher: watcher, sink: s, stop: make(chan struct{}), dirs: make(map[string]struct{})}
	go b.loop()
	go b.againLoop()
	return b, nil
}

func (b *notifyBackend) add(dir, _ string) error {
	// Noted first, so that a creation seen while the watch is being set up
	// counts as one that may have crossed it.
	b.mu.Lock()
	b.dirs[dir] = struct{}{}
	b.mu.Unlock()

	err := b.watcher.Add(dir)
	if err != nil {
		b.mu.Lock()
		delete(b.dirs, dir)
		b.mu.Unlock()
	}
	return err
}

func (b *notifyBackend) remove(dir string) {
	b.mu.Lock()
	delete(b.dirs, dir)
	b.mu.Unlock()
	_ = b.watcher.Remove(dir)
}

func (b *notifyBackend) close() error {
	close(b.stop)
	return b.watcher.Close()
}

// again asks for the watch on dir once more, if dir is still watched.
func (b *notifyBackend) again(dir string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.dirs[dir]; ok {
		// Held across the call, so the watch cannot be removed in between
		// and then brought back by this.
		_ = b.watcher.Add(dir)
	}
}

func (b *notifyBackend) againLoop() {
	ticker := time.NewTicker(againEvery)
	defer ticker.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-ticker.C:
		}
		b.mu.Lock()
		dirs := make([]string, 0, len(b.dirs))
		for dir := range b.dirs {
			dirs = append(dirs, dir)
		}
		b.mu.Unlock()
		for _, dir := range dirs {
			b.again(dir)
		}
	}
}

func (b *notifyBackend) loop() {
	for {
		select {
		case ev, ok := <-b.watcher.Events:
			if !ok {
				return
			}
			if ev.Op == fsnotify.Chmod {
				continue // permissions and timestamps, not content
			}
			b.sink.changed(ev.Name)
			if ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
				// It may have been a watched directory.
				b.sink.gone(ev.Name)
			}
			if ev.Has(fsnotify.Create) {
				b.mu.Lock()
				_, watched := b.dirs[ev.Name]
				b.mu.Unlock()
				if watched {
					time.AfterFunc(againSoon, func() { b.again(ev.Name) })
				}
			}
		case err, ok := <-b.watcher.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				b.sink.lost("")
				continue
			}
			slog.Warn("watching files", "err", err)
		}
	}
}
