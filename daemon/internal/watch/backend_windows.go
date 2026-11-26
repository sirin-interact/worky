package watch

import (
	"encoding/binary"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

const (
	// Room for the changes that pile up between two reads. When it runs out
	// the system says so and the watchers are told to look for themselves.
	notifyBuffer = 64 << 10

	notifyMask = windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_DIR_NAME |
		windows.FILE_NOTIFY_CHANGE_LAST_WRITE | windows.FILE_NOTIFY_CHANGE_SIZE
)

// winBackend watches through ReadDirectoryChangesW.
//
// Windows refuses to rename a directory while a directory below it is open,
// so opening every watched directory would get in the way of whoever works
// in the tree. Where the caller names a root, this opens the root alone and
// watches the whole tree below it.
type winBackend struct {
	sink sink

	mu    sync.Mutex
	roots map[string]*winRoot // the directories that are open
	dirs  map[string]*winRoot // the open directory each watched one is served by
}

// winRoot is one open directory and the goroutine reading its changes.
type winRoot struct {
	path      string
	recursive bool
	refs      int // watched directories served, guarded by winBackend.mu

	handle windows.Handle
	stop   windows.Handle // event that tells the reader to leave
	done   chan struct{}  // closed when the reader has left and closed handle
	dead   atomic.Bool    // the reader left on its own

	// For the reader only. The system holds their addresses while a read is
	// pending, so they must not be on a goroutine's stack, which moves.
	ov  windows.Overlapped
	buf []byte
}

func newBackend(s sink) (backend, error) {
	return &winBackend{sink: s, roots: make(map[string]*winRoot), dirs: make(map[string]*winRoot)}, nil
}

func rootKey(path string, recursive bool) string {
	if recursive {
		return key(path) + "|tree"
	}
	return key(path)
}

func (b *winBackend) add(dir, root string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	path, recursive := dir, false
	if root != "" && (key(dir) == key(root) || strings.HasPrefix(key(dir), under(key(root)))) {
		path, recursive = root, true
	}
	rk := rootKey(path, recursive)

	if old := b.dirs[key(dir)]; old != nil {
		if old == b.roots[rk] && !old.dead.Load() {
			return nil // watched already
		}
		// Served by a directory that is gone, or that is not the one to
		// use any more.
		if leaving := b.releaseLocked(dir); leaving != nil {
			go leaving.closed()
		}
	}
	r := b.roots[rk]
	if r == nil || r.dead.Load() {
		var err error
		if r, err = b.open(path, recursive); err != nil {
			return err
		}
		b.roots[rk] = r
	}
	r.refs++
	b.dirs[key(dir)] = r
	return nil
}

// closed waits for the reader to leave and frees what is left.
func (r *winRoot) closed() {
	<-r.done
	windows.CloseHandle(r.stop)
}

func (b *winBackend) remove(dir string) {
	b.mu.Lock()
	leaving := b.releaseLocked(dir)
	b.mu.Unlock()
	// Wait until the directory is closed, so the caller can go on to rename
	// or delete it.
	if leaving != nil {
		leaving.closed()
	}
}

// releaseLocked stops serving dir. If the open directory that served it
// thereby serves nothing, it is told to close and returned. The caller holds
// b.mu.
func (b *winBackend) releaseLocked(dir string) *winRoot {
	r := b.dirs[key(dir)]
	if r == nil {
		return nil
	}
	delete(b.dirs, key(dir))
	if r.refs--; r.refs > 0 {
		return nil
	}
	if rk := rootKey(r.path, r.recursive); b.roots[rk] == r {
		delete(b.roots, rk)
	}
	windows.SetEvent(r.stop)
	return r
}

func (b *winBackend) close() error {
	b.mu.Lock()
	seen := make(map[*winRoot]bool)
	for _, r := range b.dirs {
		seen[r] = true
	}
	for _, r := range b.roots {
		seen[r] = true
	}
	b.roots, b.dirs = make(map[string]*winRoot), make(map[string]*winRoot)
	b.mu.Unlock()

	for r := range seen {
		windows.SetEvent(r.stop)
	}
	for r := range seen {
		r.closed()
	}
	return nil
}

// open opens a directory and starts reading its changes.
func (b *winBackend) open(path string, recursive bool) (*winRoot, error) {
	name := path
	if len(name) >= 240 && !strings.HasPrefix(name, `\\`) {
		name = `\\?\` + name // lifts the limit on the length of a path
	}
	name16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name16, windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, &pathError{op: "open", path: path, err: err}
	}
	stop, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	completed, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(handle)
		windows.CloseHandle(stop)
		return nil, err
	}

	r := &winRoot{
		path:      path,
		recursive: recursive,
		handle:    handle,
		stop:      stop,
		done:      make(chan struct{}),
		buf:       make([]byte, notifyBuffer),
	}
	r.ov.HEvent = completed
	// The system notes changes from the first read on, so the read is asked
	// for before anyone is told the directory is watched.
	if err := r.ask(); err != nil {
		windows.CloseHandle(handle)
		windows.CloseHandle(stop)
		windows.CloseHandle(completed)
		return nil, &pathError{op: "watch", path: path, err: err}
	}
	go b.read(r)
	return r, nil
}

// ask starts a read of the directory's changes. The result arrives later, in
// buf, and the event in ov says when.
func (r *winRoot) ask() error {
	err := windows.ReadDirectoryChanges(r.handle, &r.buf[0], uint32(len(r.buf)), r.recursive, notifyMask, nil, &r.ov, 0)
	if err == windows.ERROR_IO_PENDING {
		return nil
	}
	return err
}

type pathError struct {
	op, path string
	err      error
}

func (e *pathError) Error() string { return e.op + " " + e.path + ": " + e.err.Error() }
func (e *pathError) Unwrap() error { return e.err }

// read passes on the changes in an open directory until it is told to stop
// or the directory goes. A read has been asked for when it starts.
func (b *winBackend) read(r *winRoot) {
	defer close(r.done)
	defer windows.CloseHandle(r.ov.HEvent)
	defer windows.CloseHandle(r.handle)

	for {
		which, err := windows.WaitForMultipleObjects([]windows.Handle{r.ov.HEvent, r.stop}, false, windows.INFINITE)
		if err != nil || which != windows.WAIT_OBJECT_0 {
			// Told to stop. The read is ended first: the system writes to
			// buf and ov until it is.
			var n uint32
			_ = windows.CancelIoEx(r.handle, &r.ov)
			_ = windows.GetOverlappedResult(r.handle, &r.ov, &n, true)
			return
		}

		var n uint32
		err = windows.GetOverlappedResult(r.handle, &r.ov, &n, false)
		switch {
		case err == windows.ERROR_NOTIFY_ENUM_DIR, err == nil && n == 0:
			b.sink.lost(r.path) // more changes than the buffer holds
		case err == windows.ERROR_OPERATION_ABORTED:
			// The system drops a read when the thread that asked for it
			// ends. Which changes it would have carried is unknown.
			b.sink.lost(r.path)
		case err != nil:
			b.failed(r, err)
			return
		default:
			b.pass(r, r.buf[:n])
		}
		if err := r.ask(); err != nil {
			b.failed(r, err)
			return
		}
	}
}

// failed ends the watches served by a directory that can no longer be read,
// which as a rule means it was deleted.
func (b *winBackend) failed(r *winRoot, err error) {
	r.dead.Store(true)
	if err != windows.ERROR_ACCESS_DENIED {
		slog.Warn("watching a directory", "path", r.path, "err", err)
	}
	b.sink.gone(r.path)
}

// The action codes of FILE_NOTIFY_INFORMATION that say a name is no longer
// there.
const (
	actionRemoved        = 2
	actionRenamedOldName = 4
)

// pass reports the changes in a buffer of FILE_NOTIFY_INFORMATION records:
// the offset of the next record, an action, the length of a name in bytes,
// and the name in UTF-16, relative to the open directory.
func (b *winBackend) pass(r *winRoot, data []byte) {
	for offset := 0; offset+12 <= len(data); {
		next := int(binary.LittleEndian.Uint32(data[offset:]))
		action := binary.LittleEndian.Uint32(data[offset+4:])
		length := int(binary.LittleEndian.Uint32(data[offset+8:]))
		raw := data[offset+12:]
		if length > len(raw) {
			return
		}
		name := make([]uint16, length/2)
		for i := range name {
			name[i] = binary.LittleEndian.Uint16(raw[2*i:])
		}
		path := filepath.Join(r.path, string(utf16.Decode(name)))

		b.sink.changed(path)
		if action == actionRemoved || action == actionRenamedOldName {
			b.sink.gone(path)
		}
		if next == 0 {
			return
		}
		offset += next
	}
}
