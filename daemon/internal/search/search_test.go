package search

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/sirin-interact/worky/daemon/internal/protocol"
)

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

func TestSearchNames(t *testing.T) {
	list := []string{"src/tree/index.ts", "src/ui/ProjectTree.tsx", "docs/tree.md", "README.md"}
	got, truncated := searchNames(list, "tree", false, 10)
	want := []string{"docs/tree.md", "src/ui/ProjectTree.tsx", "src/tree/index.ts"}
	if !reflect.DeepEqual(got, want) || truncated {
		t.Errorf("got %v truncated=%v, want %v", got, truncated, want)
	}
	if got, _ := searchNames(list, "Tree", true, 10); !reflect.DeepEqual(got, []string{"src/ui/ProjectTree.tsx"}) {
		t.Errorf("case sensitive: %v", got)
	}
	if got, truncated := searchNames(list, "tree", false, 2); len(got) != 2 || !truncated {
		t.Errorf("limited: %v truncated=%v", got, truncated)
	}
}

func TestSearchContent(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a.txt", "nothing here\r\nRollup of counts\r\n")
	put(t, root, "sub/b.txt", "한글 다음에 rollup 이 있다\nİstanbul rollup\n")
	put(t, root, "bin.dat", "rollup\x00binary")
	put(t, root, "node_modules/x.txt", "rollup")
	put(t, root, "long.txt", strings.Repeat("가", 600)+"rollup"+strings.Repeat("나", 600))
	ctx := context.Background()

	res, perr := Search(ctx, protocol.FsSearchParams{Root: root, Query: "ROLLUP", Kind: protocol.SearchContent})
	if perr != nil {
		t.Fatal(perr)
	}
	byPath := map[string][]protocol.SearchMatch{}
	for _, m := range res.Matches {
		byPath[m.Path] = append(byPath[m.Path], m)
		// What the offsets point at must be the match, counted the way a
		// JavaScript string counts.
		units := utf16.Encode([]rune(m.Text))
		if got := string(utf16.Decode(units[m.Start:m.End])); !strings.EqualFold(got, "rollup") {
			t.Errorf("%s:%d marks %q in %q", m.Path, m.Line, got, m.Text)
		}
	}
	if m := byPath["a.txt"]; len(m) != 1 || m[0].Line != 2 || m[0].Text != "Rollup of counts" {
		t.Errorf("a.txt: %+v", m)
	}
	if m := byPath["sub/b.txt"]; len(m) != 2 {
		t.Errorf("sub/b.txt: %+v", m)
	}
	if m := byPath["long.txt"]; len(m) != 1 || len(m[0].Text) > maxMatchText+8 {
		t.Errorf("the long line was not cut: %+v", m)
	}
	if len(byPath["bin.dat"]) != 0 || len(byPath["node_modules/x.txt"]) != 0 {
		t.Errorf("a binary or skipped file was searched: %v", res.Matches)
	}

	res, _ = Search(ctx, protocol.FsSearchParams{Root: root, Query: "rollup", Kind: protocol.SearchContent, CaseSensitive: true})
	for _, m := range res.Matches {
		if m.Path == "a.txt" {
			t.Errorf("case sensitive search matched %q", m.Text)
		}
	}

	res, _ = Search(ctx, protocol.FsSearchParams{Root: root, Query: "rollup", Kind: protocol.SearchContent, Limit: 1})
	if len(res.Matches) != 1 || !res.Truncated {
		t.Errorf("limited: %d matches, truncated=%v", len(res.Matches), res.Truncated)
	}

	res, perr = Search(ctx, protocol.FsSearchParams{Root: root, Query: "B.TXT", Kind: protocol.SearchName})
	if perr != nil || !reflect.DeepEqual(res.Files, []string{"sub/b.txt"}) {
		t.Errorf("by name: %v, %v", res.Files, perr)
	}

	if _, perr := Search(ctx, protocol.FsSearchParams{Root: root, Query: "x", Kind: "regex"}); perr == nil || perr.Code != protocol.ErrInvalidRequest {
		t.Errorf("an unknown kind: %v", perr)
	}
	if _, perr := Search(ctx, protocol.FsSearchParams{Root: "relative", Query: "x", Kind: protocol.SearchName}); perr == nil || perr.Code != protocol.ErrInvalidRequest {
		t.Errorf("a relative root: %v", perr)
	}
	if _, perr := Search(ctx, protocol.FsSearchParams{Root: filepath.Join(root, "missing"), Query: "x", Kind: protocol.SearchName}); perr == nil || perr.Code != protocol.ErrNotFound {
		t.Errorf("a missing root: %v", perr)
	}

	gone, cancel := context.WithCancel(ctx)
	cancel()
	if _, perr := Search(gone, protocol.FsSearchParams{Root: root, Query: "rollup", Kind: protocol.SearchContent}); perr == nil {
		t.Error("a cancelled search returned a result")
	}
}
