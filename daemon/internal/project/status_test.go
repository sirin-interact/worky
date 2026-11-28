package project

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sirin-interact/worky/daemon/internal/protocol"
)

func TestParseStatus(t *testing.T) {
	out := "# branch.oid abc123\x00# branch.head ui/tree\x00# branch.upstream origin/ui/tree\x00# branch.ab +2 -1\x00" +
		"1 .M N... 100644 100644 100644 aaa bbb src/a file.ts\x00" +
		"1 A. N... 000000 100644 100644 000 ccc src/new.ts\x00" +
		"2 R. N... 100644 100644 100644 ddd eee R100 src/to.ts\x00src/from.ts\x00" +
		"u UU N... 100644 100644 100644 100644 fff ggg hhh src/both.ts\x00" +
		"? notes.md\x00"
	var got protocol.GitStatusResult
	parseStatus(out, &got)
	want := protocol.GitStatusResult{
		Head: "abc123", Branch: new("ui/tree"), Upstream: new("origin/ui/tree"), Ahead: 2, Behind: 1,
		Files: []protocol.GitFile{
			{Path: "src/a file.ts", Index: ".", Worktree: "M"},
			{Path: "src/new.ts", Index: "A", Worktree: "."},
			{Path: "src/to.ts", OrigPath: "src/from.ts", Index: "R", Worktree: "."},
			{Path: "src/both.ts", Index: "U", Worktree: "U"},
			{Path: "notes.md", Index: "?", Worktree: "?"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}

	var fresh protocol.GitStatusResult
	parseStatus("# branch.oid (initial)\x00# branch.head (detached)\x00", &fresh)
	if fresh.Head != "" || fresh.Branch != nil || fresh.Files == nil || len(fresh.Files) != 0 {
		t.Errorf("an empty repository came out as %+v", fresh)
	}
}

func TestParseNumstat(t *testing.T) {
	out := "18\t4\tsrc/a.ts\x00-\t-\timg.png\x003\t1\t\x00src/from.ts\x00src/to.ts\x00"
	got := parseNumstat(out)
	want := map[string][2]int{"src/a.ts": {18, 4}, "img.png": {0, 0}, "src/to.ts": {3, 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func put(t *testing.T, root, name, text string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func changed(files []protocol.GitFile, path string) *protocol.GitFile {
	for i := range files {
		if files[i].Path == path {
			return &files[i]
		}
	}
	return nil
}

// identity lets Commit, which runs git the way a user's machine has it set
// up, work on a machine where nobody has told git who they are.
func identity(t *testing.T) {
	t.Helper()
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
}

func TestStatusStageCommit(t *testing.T) {
	identity(t)
	repo := newRepo(t, "app")
	put(t, repo, "a.txt", "one\ntwo\n")
	put(t, repo, "gone.txt", "bye\n")
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-m", "second")

	put(t, repo, "a.txt", "one\ntwo\nthree\n")
	put(t, repo, "docs/new file.md", "# new\n\ntext")
	if err := os.Remove(filepath.Join(repo, "gone.txt")); err != nil {
		t.Fatal(err)
	}

	// Asked from a directory below the root, the answer is still about the
	// whole worktree.
	st, perr := Status(filepath.Join(repo, "docs"))
	if perr != nil {
		t.Fatal(perr)
	}
	if st.Root != repo {
		t.Errorf("root is %q, want %q", st.Root, repo)
	}
	if st.Branch == nil || *st.Branch != "main" || st.Head == "" {
		t.Errorf("branch %v head %q", st.Branch, st.Head)
	}
	if f := changed(st.Files, "a.txt"); f == nil || f.Worktree != "M" || f.Index != "." || f.Added != 1 || f.Removed != 0 {
		t.Errorf("a.txt: %+v", f)
	}
	if f := changed(st.Files, "docs/new file.md"); f == nil || f.Index != "?" || f.Added != 3 {
		t.Errorf("the new file: %+v", f)
	}
	if f := changed(st.Files, "gone.txt"); f == nil || f.Worktree != "D" || f.Removed != 1 {
		t.Errorf("gone.txt: %+v", f)
	}

	if perr := Stage(repo, []string{"a.txt", "gone.txt", "docs/new file.md"}); perr != nil {
		t.Fatal(perr)
	}
	if perr := Unstage(repo, []string{"docs/new file.md"}); perr != nil {
		t.Fatal(perr)
	}
	st, _ = Status(repo)
	if f := changed(st.Files, "a.txt"); f == nil || f.Index != "M" || f.Worktree != "." {
		t.Errorf("a.txt after staging: %+v", f)
	}
	if f := changed(st.Files, "gone.txt"); f == nil || f.Index != "D" {
		t.Errorf("gone.txt after staging: %+v", f)
	}
	if f := changed(st.Files, "docs/new file.md"); f == nil || f.Index != "?" {
		t.Errorf("the new file after unstaging: %+v", f)
	}

	before := st.Head
	head, perr := Commit(repo, "fix: change things")
	if perr != nil {
		t.Fatal(perr)
	}
	if head == "" || head == before {
		t.Errorf("head did not move: %q", head)
	}
	if _, perr := Commit(repo, "  "); perr == nil || perr.Code != protocol.ErrInvalidRequest {
		t.Errorf("an empty message: %v", perr)
	}
	st, _ = Status(repo)
	if len(st.Files) != 1 || st.Files[0].Path != "docs/new file.md" {
		t.Errorf("after the commit: %+v", st.Files)
	}

	if _, perr := Status(tempDir(t)); perr == nil || perr.Code != protocol.ErrNotARepository {
		t.Errorf("outside a repository: %v", perr)
	}
	if _, perr := Status("relative"); perr == nil || perr.Code != protocol.ErrInvalidRequest {
		t.Errorf("a relative path: %v", perr)
	}
}

func TestListFiles(t *testing.T) {
	repo := newRepo(t, "app")
	put(t, repo, ".gitignore", "ignored/\n")
	put(t, repo, "ignored/x.txt", "x")
	put(t, repo, "src/b.txt", "b")
	files, err := ListFiles(repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".gitignore", "README.md", "src/b.txt"}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("got %v, want %v", files, want)
	}
}
