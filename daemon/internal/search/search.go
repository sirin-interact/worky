// Package search finds files by name and lines by content under a directory.
package search

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/sirin-interact/worky/daemon/internal/files"
	"github.com/sirin-interact/worky/daemon/internal/project"
	"github.com/sirin-interact/worky/daemon/internal/protocol"
)

const (
	defaultNameLimit    = 200
	defaultContentLimit = 500
	maxLimit            = 2000
	// A file larger than this is not searched.
	maxSearchedSize = 1 << 20
	// Without git to say which files are ignored, a walk stops after this
	// many files.
	maxWalked = 50000
	// The longest piece of a line a match is shown in, in bytes.
	maxMatchText = 400
)

// candidates returns the files under root worth searching: the ones git does
// not ignore, or every file but a few well-known heavy directories when root
// is not in a repository.
func candidates(ctx context.Context, root string) ([]string, error) {
	if list, err := project.ListFiles(root); err == nil {
		return list, nil
	}
	var list []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable, skip it
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "target", "dist", ".venv":
				return filepath.SkipDir
			}
			return nil
		}
		if rel, err := filepath.Rel(root, path); err == nil {
			list = append(list, filepath.ToSlash(rel))
		}
		if len(list) >= maxWalked {
			return filepath.SkipAll
		}
		return nil
	})
	return list, err
}

// Search looks for files by name or for lines by content under a root. It
// gives up when ctx is done.
func Search(ctx context.Context, p protocol.FsSearchParams) (protocol.FsSearchResult, *protocol.Error) {
	var res protocol.FsSearchResult
	root, perr := files.Clean(p.Root)
	if perr != nil {
		return res, perr
	}
	if p.Kind != protocol.SearchName && p.Kind != protocol.SearchContent {
		return res, protocol.Errorf(protocol.ErrInvalidRequest, `kind must be "name" or "content"`)
	}
	info, err := os.Stat(root)
	if err != nil {
		return res, files.Error(err)
	}
	if !info.IsDir() {
		return res, protocol.Errorf(protocol.ErrInvalidRequest, "%q is not a directory", root)
	}
	if p.Query == "" {
		return res, nil
	}
	list, err := candidates(ctx, root)
	if err != nil {
		return res, protocol.Errorf(protocol.ErrInternal, "the search was cut short: %v", err)
	}

	limit := p.Limit
	if p.Kind == protocol.SearchName {
		if limit <= 0 {
			limit = defaultNameLimit
		}
		res.Files, res.Truncated = searchNames(list, p.Query, p.CaseSensitive, min(limit, maxLimit))
		return res, nil
	}
	if limit <= 0 {
		limit = defaultContentLimit
	}
	res.Matches, res.Truncated, err = searchContent(ctx, root, list, p.Query, p.CaseSensitive, min(limit, maxLimit))
	if err != nil {
		return res, protocol.Errorf(protocol.ErrInternal, "the search was cut short: %v", err)
	}
	return res, nil
}

// searchNames returns the paths that contain query. A match in the file's own
// name comes before one in a directory's name, and shorter paths come first.
func searchNames(list []string, query string, caseSensitive bool, limit int) (out []string, truncated bool) {
	if !caseSensitive {
		query = strings.ToLower(query)
	}
	type hit struct {
		path   string
		inName bool
	}
	var hits []hit
	for _, path := range list {
		subject := path
		if !caseSensitive {
			subject = strings.ToLower(path)
		}
		if !strings.Contains(subject, query) {
			continue
		}
		base := subject[strings.LastIndexByte(subject, '/')+1:]
		hits = append(hits, hit{path: path, inName: strings.Contains(base, query)})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].inName != hits[j].inName {
			return hits[i].inName
		}
		return len(hits[i].path) < len(hits[j].path)
	})
	if len(hits) > limit {
		hits, truncated = hits[:limit], true
	}
	out = make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.path
	}
	return out, truncated
}

func searchContent(ctx context.Context, root string, list []string, query string, caseSensitive bool, limit int) (matches []protocol.SearchMatch, truncated bool, err error) {
	needle := []byte(query)
	if !caseSensitive {
		needle = bytes.ToLower(needle)
	}
	matches = []protocol.SearchMatch{}
	for _, rel := range list {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxSearchedSize {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			continue // unreadable or binary
		}
		// Most files have no match at all. One pass over the whole file
		// rules them out before any line is looked at.
		whole := data
		if !caseSensitive {
			whole = bytes.ToLower(data)
		}
		if !bytes.Contains(whole, needle) {
			continue
		}

		line := 0
		for len(data) > 0 {
			line++
			text := data
			if i := bytes.IndexByte(data, '\n'); i >= 0 {
				text, data = data[:i], data[i+1:]
			} else {
				data = nil
			}
			text = bytes.TrimSuffix(text, []byte{'\r'})
			start, end, ok := find(text, needle, caseSensitive)
			if !ok {
				continue
			}
			if len(matches) >= limit {
				return matches, true, nil
			}
			matches = append(matches, matchOf(rel, line, text, start, end))
		}
	}
	return matches, false, nil
}

// find returns where needle is in line, as byte offsets. Without case
// sensitivity, needle must already be in lower case.
func find(line, needle []byte, caseSensitive bool) (start, end int, ok bool) {
	if caseSensitive {
		i := bytes.Index(line, needle)
		return i, i + len(needle), i >= 0
	}
	lower := bytes.ToLower(line)
	i := bytes.Index(lower, needle)
	if i < 0 {
		return 0, 0, false
	}
	if len(lower) == len(line) {
		return i, i + len(needle), true
	}
	// Lower-casing changed the length of some character, so byte offsets
	// in one do not hold in the other. Characters still line up.
	start = advance(line, utf8.RuneCount(lower[:i]))
	end = start + advance(line[start:], utf8.RuneCount(needle))
	return start, end, true
}

// advance returns the byte length of the first n characters of p.
func advance(p []byte, n int) int {
	at := 0
	for ; n > 0 && at < len(p); n-- {
		_, size := utf8.DecodeRune(p[at:])
		at += size
	}
	return at
}

func matchOf(path string, line int, text []byte, start, end int) protocol.SearchMatch {
	// A long line is cut to a window around the match, on character
	// boundaries.
	from, to := 0, len(text)
	if len(text) > maxMatchText {
		from = max(0, start-maxMatchText/4)
		for from > 0 && !utf8.RuneStart(text[from]) {
			from--
		}
		to = min(len(text), from+maxMatchText)
		to = max(to, min(end, len(text)))
		for to < len(text) && !utf8.RuneStart(text[to]) {
			to++
		}
	}
	units := func(p []byte) int {
		return len(utf16.Encode([]rune(strings.ToValidUTF8(string(p), "�"))))
	}
	return protocol.SearchMatch{
		Path:  path,
		Line:  line,
		Text:  strings.ToValidUTF8(string(text[from:to]), "�"),
		Start: units(text[from:start]),
		End:   units(text[from:end]),
	}
}
