package files

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sirin-interact/worky/daemon/internal/protocol"
)

func put(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func wantCode(t *testing.T, what string, perr *protocol.Error, code protocol.ErrorCode) {
	t.Helper()
	if perr == nil || perr.Code != code {
		t.Errorf("%s: %v, want %s", what, perr, code)
	}
}

func str(s string) *string { return &s }

func num(n int64) *int64 { return &n }

func describe(entries []protocol.FsEntry) string {
	var parts []string
	for _, e := range entries {
		part := e.Name + ":" + string(e.Type)
		if e.Link {
			part += ":link"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " ")
}

func TestRoots(t *testing.T) {
	r := Roots()
	if len(r.Roots) == 0 || !filepath.IsAbs(r.Home) {
		t.Fatalf("roots = %+v", r)
	}
	for _, root := range r.Roots {
		if !filepath.IsAbs(root) || filepath.Dir(root) != root {
			t.Errorf("%q is not the top of a file system", root)
		}
	}
	if _, perr := List(r.Home); perr != nil {
		t.Errorf("listing the home directory: %v", perr)
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "b.txt"), "b")
	put(t, filepath.Join(dir, "A.txt"), "a")
	put(t, filepath.Join(dir, "한글.md"), "가")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	// The path comes back in one spelling whatever was sent.
	got, perr := List(dir + string(filepath.Separator) + "sub" + string(filepath.Separator) + "..")
	if perr != nil {
		t.Fatal(perr)
	}
	if got.Path != dir || got.Truncated {
		t.Errorf("path = %q truncated = %v, want %q", got.Path, got.Truncated, dir)
	}
	if want := "A.txt:file b.txt:file sub:dir 한글.md:file"; describe(got.Entries) != want {
		t.Errorf("entries = %s, want %s", describe(got.Entries), want)
	}

	empty, perr := List(filepath.Join(dir, "sub"))
	if perr != nil || empty.Entries == nil || len(empty.Entries) != 0 {
		t.Errorf("an empty directory: %+v, %v", empty, perr)
	}

	_, perr = List(filepath.Join(dir, "missing"))
	wantCode(t, "a missing directory", perr, protocol.ErrNotFound)
	_, perr = List(filepath.Join(dir, "b.txt"))
	wantCode(t, "a file", perr, protocol.ErrInvalidRequest)
	_, perr = List("sub")
	wantCode(t, "a relative path", perr, protocol.ErrInvalidRequest)
}

func TestListTruncates(t *testing.T) {
	full := listBudget
	listBudget = 4 << 10
	defer func() { listBudget = full }()

	dir := t.TempDir()
	// Names that grow when they are written as JSON.
	name := strings.Repeat("a&b", 20)
	const count = 100
	for i := range count {
		put(t, filepath.Join(dir, fmt.Sprintf("%s%03d", name, i)), "")
	}
	got, perr := List(dir)
	if perr != nil {
		t.Fatal(perr)
	}
	if !got.Truncated || len(got.Entries) == 0 || len(got.Entries) >= count {
		t.Fatalf("truncated = %v with %d of %d entries", got.Truncated, len(got.Entries), count)
	}
	if wire, _ := json.Marshal(got); len(wire) > listBudget+256 {
		t.Errorf("the listing takes %d bytes on the wire, with %d allowed for its entries", len(wire), listBudget)
	}
	if first := got.Entries[0].Name; first != name+"000" {
		t.Errorf("the entries that were kept start at %q", first)
	}
}

// link makes a link to a directory or a file without needing rights the
// tests may not have. It reports false when it cannot.
func link(t *testing.T, target, at string, dir bool) bool {
	t.Helper()
	if runtime.GOOS == "windows" {
		if !dir {
			return os.Symlink(target, at) == nil // needs developer mode
		}
		// A junction, as package managers make them.
		return exec.Command("cmd", "/c", "mklink", "/J", at, target).Run() == nil
	}
	return os.Symlink(target, at) == nil
}

func TestLinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(dir, "real", "inside.txt"), "x")
	put(t, filepath.Join(dir, "file.txt"), "hello")

	want := "file.txt:file real:dir"
	if link(t, filepath.Join(dir, "real"), filepath.Join(dir, "to-dir"), true) {
		want += " to-dir:dir:link"
		st, perr := Stat(filepath.Join(dir, "to-dir"))
		if perr != nil || st.Type != protocol.TypeDir || !st.Link {
			t.Errorf("stat of a link to a directory: %+v, %v", st, perr)
		}
		through, perr := List(filepath.Join(dir, "to-dir"))
		if perr != nil || describe(through.Entries) != "inside.txt:file" {
			t.Errorf("listing through the link: %+v, %v", through, perr)
		}
	} else {
		t.Log("cannot make a link to a directory here")
	}
	if link(t, filepath.Join(dir, "file.txt"), filepath.Join(dir, "to-file"), false) {
		want += " to-file:file:link"
		st, perr := Stat(filepath.Join(dir, "to-file"))
		if perr != nil || st.Type != protocol.TypeFile || !st.Link || st.Size != 5 {
			t.Errorf("stat of a link to a file: %+v, %v", st, perr)
		}

		// Writing through a link changes what it leads to and keeps the link.
		if _, perr := Write(protocol.FsWriteParams{Path: filepath.Join(dir, "to-file"), Data: []byte("changed")}); perr != nil {
			t.Fatal(perr)
		}
		if data, _ := os.ReadFile(filepath.Join(dir, "file.txt")); string(data) != "changed" {
			t.Errorf("the file behind the link holds %q", data)
		}
		if info, err := os.Lstat(filepath.Join(dir, "to-file")); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Error("the link was replaced by a file")
		}

		if os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "to-nothing")) == nil {
			want += " to-nothing:other:link"
		}
	} else {
		t.Log("cannot make a link to a file here")
	}

	got, perr := List(dir)
	if perr != nil {
		t.Fatal(perr)
	}
	if describe(got.Entries) != want {
		t.Errorf("entries = %s, want %s", describe(got.Entries), want)
	}

	// Removing a link leaves what it leads to.
	if _, err := os.Lstat(filepath.Join(dir, "to-dir")); err == nil {
		if perr := Remove(filepath.Join(dir, "to-dir"), true); perr != nil {
			t.Fatal(perr)
		}
		if _, err := os.Stat(filepath.Join(dir, "real", "inside.txt")); err != nil {
			t.Errorf("removing the link removed what it led to: %v", err)
		}
	}
}

func TestStat(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	put(t, file, "12345")

	st, perr := Stat(file)
	if perr != nil {
		t.Fatal(perr)
	}
	if st.Path != file || st.Type != protocol.TypeFile || st.Link || st.Size != 5 || st.Version == "" || !strings.HasSuffix(st.Mtime, "Z") {
		t.Errorf("stat = %+v", st)
	}
	if d, perr := Stat(dir); perr != nil || d.Type != protocol.TypeDir {
		t.Errorf("stat of a directory: %+v, %v", d, perr)
	}
	_, perr = Stat(filepath.Join(dir, "missing"))
	wantCode(t, "a missing file", perr, protocol.ErrNotFound)
}

func TestRead(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.bin")
	// Bytes that are not text in any encoding.
	content := bytes.Repeat([]byte{0x00, 0xff, 0xfe, 0x80, '\r', '\n'}, 1000)
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatal(err)
	}

	whole, perr := Read(protocol.FsReadParams{Path: file})
	if perr != nil {
		t.Fatal(perr)
	}
	if !bytes.Equal(whole.Data, content) || whole.Size != int64(len(content)) || whole.Version == "" {
		t.Errorf("read %d bytes of %d, size %d", len(whole.Data), len(content), whole.Size)
	}

	part, perr := Read(protocol.FsReadParams{Path: file, Offset: 10, Length: num(7)})
	if perr != nil || !bytes.Equal(part.Data, content[10:17]) || part.Size != int64(len(content)) || part.Version != whole.Version {
		t.Errorf("a part: %+v, %v", part, perr)
	}
	end, perr := Read(protocol.FsReadParams{Path: file, Offset: int64(len(content)) - 3, Length: num(100)})
	if perr != nil || !bytes.Equal(end.Data, content[len(content)-3:]) {
		t.Errorf("the end: %+v, %v", end, perr)
	}
	past, perr := Read(protocol.FsReadParams{Path: file, Offset: int64(len(content)) + 5})
	if perr != nil || past.Data == nil || len(past.Data) != 0 {
		t.Errorf("past the end: %+v, %v", past, perr)
	}
	none, perr := Read(protocol.FsReadParams{Path: file, Length: num(0)})
	if perr != nil || len(none.Data) != 0 || none.Size != int64(len(content)) {
		t.Errorf("no bytes: %+v, %v", none, perr)
	}

	_, perr = Read(protocol.FsReadParams{Path: file, Offset: -1})
	wantCode(t, "a negative offset", perr, protocol.ErrInvalidRequest)
	_, perr = Read(protocol.FsReadParams{Path: dir})
	wantCode(t, "a directory", perr, protocol.ErrInvalidRequest)
	_, perr = Read(protocol.FsReadParams{Path: filepath.Join(dir, "missing")})
	wantCode(t, "a missing file", perr, protocol.ErrNotFound)
}

func TestReadLongFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "long")
	content := bytes.Repeat([]byte("0123456789abcdef"), (2*protocol.MaxFileChunk+1000)/16)
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatal(err)
	}

	var got []byte
	version := ""
	for calls := 0; ; calls++ {
		// Asking for more than one call carries gets what one call carries.
		part, perr := Read(protocol.FsReadParams{Path: file, Offset: int64(len(got)), Length: num(64 << 20)})
		if perr != nil {
			t.Fatal(perr)
		}
		if len(part.Data) > protocol.MaxFileChunk {
			t.Fatalf("one call returned %d bytes", len(part.Data))
		}
		if version != "" && part.Version != version {
			t.Fatal("the version changed between calls")
		}
		version = part.Version
		got = append(got, part.Data...)
		if int64(len(got)) >= part.Size {
			if calls != 2 {
				t.Errorf("took %d calls, want 3", calls+1)
			}
			break
		}
	}
	if !bytes.Equal(got, content) {
		t.Error("the parts do not add up to the file")
	}
}

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")

	// A new file.
	first, perr := Write(protocol.FsWriteParams{Path: file, Data: []byte("one")})
	if perr != nil {
		t.Fatal(perr)
	}
	st, _ := Stat(file)
	if first.Size != 3 || first.Version == "" || first.Version != st.Version {
		t.Errorf("write returned %+v, stat says %+v", first, st)
	}
	if read, _ := Read(protocol.FsReadParams{Path: file}); read.Version != first.Version || string(read.Data) != "one" {
		t.Errorf("read back %+v", read)
	}

	// At the expected version, and replacing longer content with shorter.
	second, perr := Write(protocol.FsWriteParams{Path: file, Data: []byte("2"), Expect: &first.Version})
	if perr != nil {
		t.Fatal(perr)
	}
	if data, _ := os.ReadFile(file); string(data) != "2" || second.Size != 1 || second.Version == first.Version {
		t.Errorf("after the second write the file holds %q, result %+v", data, second)
	}

	// Somebody else wrote in between.
	put(t, file, "theirs, and longer")
	_, perr = Write(protocol.FsWriteParams{Path: file, Data: []byte("mine"), Expect: &second.Version})
	wantCode(t, "a stale version", perr, protocol.ErrConflict)
	if data, _ := os.ReadFile(file); string(data) != "theirs, and longer" {
		t.Errorf("a refused write changed the file to %q", data)
	}

	// Or deleted it.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	_, perr = Write(protocol.FsWriteParams{Path: file, Data: []byte("mine"), Expect: &second.Version})
	wantCode(t, "an expected file that is gone", perr, protocol.ErrNotFound)
	if _, err := os.Stat(file); err == nil {
		t.Error("a refused write created the file")
	}

	// Only if nothing is there.
	if _, perr := Write(protocol.FsWriteParams{Path: file, Exclusive: true}); perr != nil {
		t.Fatal(perr)
	}
	_, perr = Write(protocol.FsWriteParams{Path: file, Data: []byte("x"), Exclusive: true})
	wantCode(t, "exclusive over a file", perr, protocol.ErrAlreadyExists)

	_, perr = Write(protocol.FsWriteParams{Path: file, Exclusive: true, Append: true})
	wantCode(t, "exclusive with append", perr, protocol.ErrInvalidRequest)
	_, perr = Write(protocol.FsWriteParams{Path: file, Exclusive: true, Expect: str("x")})
	wantCode(t, "exclusive with expect", perr, protocol.ErrInvalidRequest)
	_, perr = Write(protocol.FsWriteParams{Path: dir, Data: []byte("x")})
	wantCode(t, "a directory", perr, protocol.ErrInvalidRequest)
	_, perr = Write(protocol.FsWriteParams{Path: filepath.Join(dir, "no", "such", "f"), Data: []byte("x")})
	wantCode(t, "a missing directory", perr, protocol.ErrNotFound)
	_, perr = Write(protocol.FsWriteParams{Path: file, Data: make([]byte, protocol.MaxFileChunk+1)})
	wantCode(t, "too much in one call", perr, protocol.ErrInvalidRequest)
}

func TestWriteInParts(t *testing.T) {
	file := filepath.Join(t.TempDir(), "long")
	put(t, file, "what was there before, which is longer than the first part")
	content := bytes.Repeat([]byte("0123456789abcdef"), (2*protocol.MaxFileChunk+1000)/16)

	var expect *string
	for offset := 0; offset < len(content); offset += protocol.MaxFileChunk {
		part := content[offset:min(offset+protocol.MaxFileChunk, len(content))]
		res, perr := Write(protocol.FsWriteParams{Path: file, Data: part, Append: offset > 0, Expect: expect})
		if perr != nil {
			t.Fatal(perr)
		}
		if res.Size != int64(offset+len(part)) {
			t.Fatalf("after writing up to %d the file has %d bytes", offset+len(part), res.Size)
		}
		expect = &res.Version
	}
	if data, _ := os.ReadFile(file); !bytes.Equal(data, content) {
		t.Error("the parts do not add up to the content")
	}

	// A part does not go onto a file somebody else has changed.
	put(t, file, "theirs")
	_, perr := Write(protocol.FsWriteParams{Path: file, Data: []byte("more"), Append: true, Expect: expect})
	wantCode(t, "appending to a changed file", perr, protocol.ErrConflict)
}

func TestWriteKeepsPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no permission bits to keep")
	}
	file := filepath.Join(t.TempDir(), "run.sh")
	if err := os.WriteFile(file, []byte("#!/bin/sh\n"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, perr := Write(protocol.FsWriteParams{Path: file, Data: []byte("#!/bin/sh\necho\n")}); perr != nil {
		t.Fatal(perr)
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o750 {
		t.Errorf("permissions are %o, want 750", info.Mode().Perm())
	}

	if os.Getuid() == 0 {
		return // nothing is denied to root
	}
	if err := os.Chmod(file, 0o400); err != nil {
		t.Fatal(err)
	}
	_, perr := Write(protocol.FsWriteParams{Path: file, Data: []byte("x")})
	wantCode(t, "a read-only file", perr, protocol.ErrPermissionDenied)
}

func TestMkdir(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b", "c")
	if perr := Mkdir(deep); perr != nil {
		t.Fatal(perr)
	}
	if info, err := os.Stat(deep); err != nil || !info.IsDir() {
		t.Errorf("the directory is not there: %v", err)
	}
	wantCode(t, "a directory that exists", Mkdir(deep), protocol.ErrAlreadyExists)
	put(t, filepath.Join(dir, "f"), "")
	wantCode(t, "a file in the way", Mkdir(filepath.Join(dir, "f")), protocol.ErrAlreadyExists)
	wantCode(t, "a relative path", Mkdir("a"), protocol.ErrInvalidRequest)
}

func TestRename(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "a.txt"), "a")
	put(t, filepath.Join(dir, "b.txt"), "b")
	if err := os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}

	if perr := Rename(filepath.Join(dir, "a.txt"), filepath.Join(dir, "sub", "c.txt")); perr != nil {
		t.Fatal(perr)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "sub", "c.txt")); string(data) != "a" {
		t.Errorf("the moved file holds %q", data)
	}
	if perr := Rename(filepath.Join(dir, "sub"), filepath.Join(dir, "moved")); perr != nil {
		t.Fatal(perr)
	}
	if _, err := os.Stat(filepath.Join(dir, "moved", "deep")); err != nil {
		t.Errorf("the moved directory lost its contents: %v", err)
	}

	// Never over something else.
	wantCode(t, "over a file", Rename(filepath.Join(dir, "moved", "c.txt"), filepath.Join(dir, "b.txt")), protocol.ErrAlreadyExists)
	if data, _ := os.ReadFile(filepath.Join(dir, "b.txt")); string(data) != "b" {
		t.Errorf("a refused rename changed the target to %q", data)
	}
	wantCode(t, "a missing file", Rename(filepath.Join(dir, "missing"), filepath.Join(dir, "x")), protocol.ErrNotFound)

	// Changing only the case of a name works where case does not count too.
	if perr := Rename(filepath.Join(dir, "b.txt"), filepath.Join(dir, "B.txt")); perr != nil {
		t.Fatal(perr)
	}
	got, _ := List(dir)
	if describe(got.Entries) != "B.txt:file moved:dir" {
		t.Errorf("entries = %s", describe(got.Entries))
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "f"), "x")
	if err := os.MkdirAll(filepath.Join(dir, "full", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(dir, "full", "deep", "g"), "x")
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	if perr := Remove(filepath.Join(dir, "f"), false); perr != nil {
		t.Fatal(perr)
	}
	if perr := Remove(filepath.Join(dir, "empty"), false); perr != nil {
		t.Fatal(perr)
	}
	if perr := Remove(filepath.Join(dir, "full"), false); perr == nil {
		t.Error("a full directory went without recursive")
	}
	if _, err := os.Stat(filepath.Join(dir, "full", "deep", "g")); err != nil {
		t.Errorf("a refused remove deleted something: %v", err)
	}
	if perr := Remove(filepath.Join(dir, "full"), true); perr != nil {
		t.Fatal(perr)
	}
	if got, _ := List(dir); len(got.Entries) != 0 {
		t.Errorf("left over: %s", describe(got.Entries))
	}

	wantCode(t, "a missing file", Remove(filepath.Join(dir, "f"), true), protocol.ErrNotFound)
	for _, root := range Roots().Roots {
		wantCode(t, "the top of a file system", Remove(root, true), protocol.ErrInvalidRequest)
	}
}
