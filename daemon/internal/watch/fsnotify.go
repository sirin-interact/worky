//go:build !windows

package watch

import (
	"errors"
	"log/slog"

	"github.com/fsnotify/fsnotify"
)

// notifyBackend watches each directory on its own, through fsnotify.
type notifyBackend struct {
	watcher *fsnotify.Watcher
	sink    sink
}

func newNotifyBackend(s sink) (backend, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	b := &notifyBackend{watcher: watcher, sink: s}
	go b.loop()
	return b, nil
}

func (b *notifyBackend) add(dir, _ string) error { return b.watcher.Add(dir) }

func (b *notifyBackend) remove(dir string) { _ = b.watcher.Remove(dir) }

func (b *notifyBackend) close() error { return b.watcher.Close() }

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
