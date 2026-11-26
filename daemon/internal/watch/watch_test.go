package watch

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

const waitLimit = 10 * time.Second

// seen collects what a watch reports.
type seen struct {
	mu      sync.Mutex
	names   map[string]bool
	reports int
	unknown int // reports without names
	ended   bool
}

func (s *seen) note(c Change) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.names == nil {
		s.names = make(map[string]bool)
	}
	s.reports++
	if c.Names == nil {
		s.unknown++
	}
	if !slices.IsSorted(c.Names) {
		s.names["<unsorted>"] = true
	}
	for _, name := range c.Names {
		s.names[name] = true
	}
	s.ended = s.ended || c.Ended
}

func (s *seen) has(names ...string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range names {
		if !s.names[name] {
			return false
		}
	}
	return true
}

func (s *seen) snapshot() (names []string, reports int, ended bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name := range s.names {
		names = append(names, name)
	}
	slices.Sort(names)
	return names, s.reports, s.ended
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func testWatcher(t *testing.T) *Watcher {
	t.Helper()
	w, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReportsEntriesByName(t *testing.T) {
	w := testWatcher(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "old.txt"), "1")

	var got seen
	x, err := w.Add(dir, "", got.note)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Stop()

	write(t, filepath.Join(dir, "new.txt"), "1")
	eventually(t, "the created file", func() bool { return got.has("new.txt") })

	write(t, filepath.Join(dir, "old.txt"), "22")
	eventually(t, "the written file", func() bool { return got.has("old.txt") })

	if err := os.Rename(filepath.Join(dir, "new.txt"), filepath.Join(dir, "moved.txt")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the renamed file", func() bool { return got.has("moved.txt") })

	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new directory", func() bool { return got.has("sub") })

	if err := os.Remove(filepath.Join(dir, "old.txt")); err != nil {
		t.Fatal(err)
	}
	// old.txt was reported before, so wait for one more report instead.
	_, before, _ := got.snapshot()
	eventually(t, "the deleted file", func() bool {
		_, reports, _ := got.snapshot()
		return reports > before || got.has("old.txt")
	})

	if names, _, _ := got.snapshot(); slices.Contains(names, "<unsorted>") {
		t.Error("a report's names were not sorted")
	}
}

func TestCollectsChanges(t *testing.T) {
	w := testWatcher(t)
	w.delay = 300 * time.Millisecond
	dir := t.TempDir()

	var got seen
	x, err := w.Add(dir, "", got.note)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Stop()

	for _, name := range []string{"a", "b", "c", "d"} {
		write(t, filepath.Join(dir, name), "1")
	}
	eventually(t, "all four files", func() bool { return got.has("a", "b", "c", "d") })
	if _, reports, _ := got.snapshot(); reports != 1 {
		t.Errorf("four quick changes came in %d reports, want 1", reports)
	}
}

func TestTooManyChangesAreNotNamed(t *testing.T) {
	w := testWatcher(t)
	w.delay = 500 * time.Millisecond
	dir := t.TempDir()

	var got seen
	x, err := w.Add(dir, "", got.note)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Stop()

	for i := range maxNames + 20 {
		write(t, filepath.Join(dir, "f"+string(rune('a'+i%26))+string(rune('a'+i/26))), "1")
	}
	eventually(t, "a report without names", func() bool {
		got.mu.Lock()
		defer got.mu.Unlock()
		return got.unknown > 0
	})
}

func TestDeeperChangesAreNotNamed(t *testing.T) {
	w := testWatcher(t)
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	var got seen
	x, err := w.Add(dir, "", got.note)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Stop()

	write(t, filepath.Join(sub, "deep.txt"), "1")
	write(t, filepath.Join(dir, "top.txt"), "1")
	eventually(t, "the file at the top", func() bool { return got.has("top.txt") })
	time.Sleep(2 * defaultDelay)

	// Windows reports the directory whose entries changed. Nothing reports
	// the entry inside it.
	names, _, _ := got.snapshot()
	for _, name := range names {
		if name != "top.txt" && name != "sub" {
			t.Errorf("reported %q", name)
		}
	}
}

func TestEndsWhenTheDirectoryGoes(t *testing.T) {
	for _, how := range []string{"deleted", "deleted with its parent watched"} {
		t.Run(how, func(t *testing.T) {
			w := testWatcher(t)
			parent := t.TempDir()
			dir := filepath.Join(parent, "dir")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "f"), "1")

			var above, got seen
			if how != "deleted" {
				x, err := w.Add(parent, "", above.note)
				if err != nil {
					t.Fatal(err)
				}
				defer x.Stop()
			}
			x, err := w.Add(dir, "", got.note)
			if err != nil {
				t.Fatal(err)
			}
			defer x.Stop()

			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			eventually(t, "the watch to end", func() bool {
				_, _, ended := got.snapshot()
				return ended
			})
			if how != "deleted" {
				eventually(t, "the parent to report it", func() bool { return above.has("dir") })
				if _, _, ended := above.snapshot(); ended {
					t.Error("the parent's watch ended too")
				}
			}

			// The same path can be watched again once it is back.
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			var again seen
			y, err := w.Add(dir, "", again.note)
			if err != nil {
				t.Fatal(err)
			}
			defer y.Stop()
			// The first watch's leftovers are cleared in the background.
			time.Sleep(2 * defaultDelay)
			write(t, filepath.Join(dir, "g"), "1")
			eventually(t, "a change in the new directory", func() bool { return again.has("g") })
		})
	}
}

// A directory that moved because one above it was renamed. Its own watch is
// the only one, so nothing sees the rename happen.
func TestEndsWhenADirectoryAboveMoves(t *testing.T) {
	w, err := newWatcher(100 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })

	top := t.TempDir()
	deep := filepath.Join(top, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	var got seen
	x, err := w.Add(deep, top, got.note)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Stop()

	if err := os.Rename(filepath.Join(top, "a"), filepath.Join(top, "z")); err != nil {
		t.Fatalf("renaming a directory above a watched one: %v", err)
	}
	eventually(t, "the watch to end", func() bool {
		_, _, ended := got.snapshot()
		return ended
	})

	// What happens at the new place is no longer this watch's business.
	_, before, _ := got.snapshot()
	write(t, filepath.Join(top, "z", "b", "c", "f"), "1")
	time.Sleep(3 * defaultDelay)
	if _, after, _ := got.snapshot(); after != before {
		t.Error("an ended watch kept reporting")
	}
}

// With the parent of the renamed directory watched, the end comes at once.
func TestEndsBelowARenamedDirectory(t *testing.T) {
	w := testWatcher(t)
	top := t.TempDir()
	deep := filepath.Join(top, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	var above, got seen
	x, err := w.Add(top, top, above.note)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Stop()
	y, err := w.Add(deep, top, got.note)
	if err != nil {
		t.Fatal(err)
	}
	defer y.Stop()

	if err := os.Rename(filepath.Join(top, "a"), filepath.Join(top, "z")); err != nil {
		t.Fatalf("renaming a directory above a watched one: %v", err)
	}
	began := time.Now()
	eventually(t, "the watch to end", func() bool {
		_, _, ended := got.snapshot()
		return ended
	})
	if took := time.Since(began); took > defaultCheck/2 {
		t.Errorf("the end took %v, so it came from the periodic check", took)
	}
	eventually(t, "the parent to name both", func() bool { return above.has("a", "z") })
}

func TestSharedDirectory(t *testing.T) {
	w := testWatcher(t)
	dir := t.TempDir()

	var first, second seen
	x, err := w.Add(dir, "", first.note)
	if err != nil {
		t.Fatal(err)
	}
	// Another spelling of the same directory.
	y, err := w.Add(dir+string(filepath.Separator)+".", "", second.note)
	if err != nil {
		t.Fatal(err)
	}
	defer y.Stop()

	write(t, filepath.Join(dir, "a"), "1")
	eventually(t, "both watches", func() bool { return first.has("a") && second.has("a") })

	x.Stop()
	time.Sleep(2 * defaultDelay)
	_, before, _ := first.snapshot()
	write(t, filepath.Join(dir, "b"), "1")
	eventually(t, "the watch that is left", func() bool { return second.has("b") })
	time.Sleep(2 * defaultDelay)
	if _, after, _ := first.snapshot(); after != before {
		t.Error("a stopped watch kept reporting")
	}

	y.Stop()
	w.mu.Lock()
	left := len(w.dirs)
	w.mu.Unlock()
	if left != 0 {
		t.Errorf("%d directories still watched after every watch stopped", left)
	}
}

func TestOnlyDirectories(t *testing.T) {
	w := testWatcher(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	write(t, file, "1")

	if _, err := w.Add(file, "", func(Change) {}); err == nil {
		t.Error("watching a file worked")
	}
	if _, err := w.Add(filepath.Join(dir, "missing"), "", func(Change) {}); !os.IsNotExist(err) {
		t.Errorf("watching a missing directory: %v", err)
	}
}
