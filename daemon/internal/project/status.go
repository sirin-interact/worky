package project

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sirin-interact/worky/daemon/internal/protocol"
)

// How long a commit may take. Hooks run inside it.
const commitLimit = 2 * time.Minute

// Status says what has changed in the worktree that path is in.
func Status(path string) (protocol.GitStatusResult, *protocol.Error) {
	var res protocol.GitStatusResult
	path, perr := absolute(path)
	if perr != nil {
		return res, perr
	}
	root, err := git(queryLimit, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return res, protocolError(err)
	}
	res.Root = filepath.Clean(filepath.FromSlash(strings.TrimSpace(root)))

	out, err := git(queryLimit, res.Root, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all")
	if err != nil {
		return res, protocolError(err)
	}
	parseStatus(out, &res)

	// The line counts are a nicety. A repository with no commit yet has no
	// HEAD to compare against, and that is not an error.
	if out, err := git(queryLimit, res.Root, "diff", "--numstat", "-z", "HEAD"); err == nil {
		counts := parseNumstat(out)
		for i := range res.Files {
			if n, ok := counts[res.Files[i].Path]; ok {
				res.Files[i].Added, res.Files[i].Removed = n[0], n[1]
			}
		}
	}
	countUntracked(res.Root, res.Files)
	return res, nil
}

// parseStatus reads the output of git status --porcelain=v2 --branch -z.
func parseStatus(out string, res *protocol.GitStatusResult) {
	res.Files = []protocol.GitFile{}
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		if len(field) < 2 {
			continue
		}
		switch field[0] {
		case '#':
			parts := strings.Fields(field)
			if len(parts) < 3 {
				continue
			}
			switch parts[1] {
			case "branch.oid":
				if parts[2] != "(initial)" {
					res.Head = parts[2]
				}
			case "branch.head":
				if parts[2] != "(detached)" {
					res.Branch = &parts[2]
				}
			case "branch.upstream":
				res.Upstream = &parts[2]
			case "branch.ab":
				if len(parts) >= 4 {
					res.Ahead, _ = strconv.Atoi(strings.TrimPrefix(parts[2], "+"))
					res.Behind, _ = strconv.Atoi(strings.TrimPrefix(parts[3], "-"))
				}
			}
		case '1':
			if parts := strings.SplitN(field, " ", 9); len(parts) == 9 {
				res.Files = append(res.Files, statusFile(parts[1], parts[8], ""))
			}
		case '2':
			// The path a rename came from is the next field.
			if parts := strings.SplitN(field, " ", 10); len(parts) == 10 {
				orig := ""
				if i+1 < len(fields) {
					i++
					orig = fields[i]
				}
				res.Files = append(res.Files, statusFile(parts[1], parts[9], orig))
			}
		case 'u':
			if parts := strings.SplitN(field, " ", 11); len(parts) == 11 {
				res.Files = append(res.Files, statusFile(parts[1], parts[10], ""))
			}
		case '?':
			res.Files = append(res.Files, protocol.GitFile{Path: field[2:], Index: "?", Worktree: "?"})
		}
	}
}

func statusFile(xy, path, orig string) protocol.GitFile {
	f := protocol.GitFile{Path: path, OrigPath: orig, Index: ".", Worktree: "."}
	if len(xy) == 2 {
		f.Index, f.Worktree = xy[:1], xy[1:]
	}
	return f
}

// parseNumstat reads the output of git diff --numstat -z. The result maps a
// path to the lines added and removed.
func parseNumstat(out string) map[string][2]int {
	counts := make(map[string][2]int)
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if path == "" {
			// A rename: the old and the new path are the next two fields.
			if i+2 >= len(fields) {
				break
			}
			path = fields[i+2]
			i += 2
		}
		added, _ := strconv.Atoi(parts[0])
		removed, _ := strconv.Atoi(parts[1])
		counts[path] = [2]int{added, removed}
	}
	return counts
}

const (
	maxUntrackedCounted = 200
	maxUntrackedSize    = 1 << 20
)

// countUntracked fills in the line counts of new files, which no diff
// against HEAD reports.
func countUntracked(root string, files []protocol.GitFile) {
	counted := 0
	for i := range files {
		if files[i].Index != "?" {
			continue
		}
		if counted++; counted > maxUntrackedCounted {
			return
		}
		path := filepath.Join(root, filepath.FromSlash(files[i].Path))
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxUntrackedSize {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			continue
		}
		lines := bytes.Count(data, []byte{'\n'})
		if len(data) > 0 && data[len(data)-1] != '\n' {
			lines++
		}
		files[i].Added = lines
	}
}

// index runs a git command that takes a list of files, which are relative to
// the worktree's root.
func index(path string, files []string, args ...string) *protocol.Error {
	path, perr := absolute(path)
	if perr != nil {
		return perr
	}
	if len(files) == 0 {
		return nil
	}
	// The names are taken as they are, not as patterns.
	args = append(append([]string{"--literal-pathspecs"}, args...), "--")
	if _, err := git(queryLimit, path, append(args, files...)...); err != nil {
		return protocolError(err)
	}
	return nil
}

// Stage adds files to the index, deletions included.
func Stage(path string, files []string) *protocol.Error {
	return index(path, files, "add", "-A")
}

// Unstage takes files out of the index again.
func Unstage(path string, files []string) *protocol.Error {
	return index(path, files, "reset", "-q")
}

// Commit commits what is staged and returns the new HEAD.
func Commit(path, message string) (string, *protocol.Error) {
	path, perr := absolute(path)
	if perr != nil {
		return "", perr
	}
	if strings.TrimSpace(message) == "" {
		return "", protocol.Errorf(protocol.ErrInvalidRequest, "the commit message is empty")
	}
	if _, err := git(commitLimit, path, "commit", "-m", message); err != nil {
		return "", protocolError(err)
	}
	head, err := git(queryLimit, path, "rev-parse", "HEAD")
	if err != nil {
		return "", protocolError(err)
	}
	return strings.TrimSpace(head), nil
}

// ListFiles returns the files under dir that git does not ignore, relative
// to dir with forward slashes, sorted.
func ListFiles(dir string) ([]string, error) {
	out, err := git(queryLimit, dir, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	files := strings.Split(out, "\x00")
	if n := len(files); n > 0 && files[n-1] == "" {
		files = files[:n-1]
	}
	// git lists the tracked files and the new ones apart.
	slices.Sort(files)
	return slices.Compact(files), nil
}
