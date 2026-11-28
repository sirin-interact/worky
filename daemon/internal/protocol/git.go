package protocol

// The messages for searching a directory and for what git says about a
// worktree. See packages/protocol/README.md.

// Activity says whether a session's process is producing output.
type Activity string

const (
	ActivityBusy Activity = "busy"
	ActivityIdle Activity = "idle"
)

// The kinds of fs.search.
const (
	SearchName    = "name"
	SearchContent = "content"
)

type FsSearchParams struct {
	Root          string `json:"root"`
	Query         string `json:"query"`
	Kind          string `json:"kind"`
	CaseSensitive bool   `json:"caseSensitive,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

// SearchMatch is one line that matched a content search.
type SearchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	// Text is the line, cut short when it is long. Start and End say where
	// the match is in Text, in UTF-16 code units.
	Text  string `json:"text"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// FsSearchResult has Files for a name search and Matches for a content
// search. Paths are relative to the root and use forward slashes.
type FsSearchResult struct {
	Files     []string      `json:"files,omitempty"`
	Matches   []SearchMatch `json:"matches,omitempty"`
	Truncated bool          `json:"truncated"`
}

// GitPathParams is the params of git.status.
type GitPathParams struct {
	Path string `json:"path"`
}

// GitFile is one changed file. Index and Worktree are git's status letters:
// "." for a side that is unchanged and "?" for an untracked file.
type GitFile struct {
	Path     string `json:"path"`
	OrigPath string `json:"origPath,omitempty"`
	Index    string `json:"index"`
	Worktree string `json:"worktree"`
	// Lines added and removed against HEAD. Both are zero for a binary file.
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

type GitStatusResult struct {
	// Root is the worktree the path belongs to.
	Root string `json:"root"`
	// Branch is nil when no branch is checked out.
	Branch   *string   `json:"branch"`
	Head     string    `json:"head"`
	Upstream *string   `json:"upstream"`
	Ahead    int       `json:"ahead"`
	Behind   int       `json:"behind"`
	Files    []GitFile `json:"files"`
}

// GitFilesParams is the params of git.stage and git.unstage. Files are
// relative to the worktree's root.
type GitFilesParams struct {
	Path  string   `json:"path"`
	Files []string `json:"files"`
}

type GitCommitParams struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type GitCommitResult struct {
	Head string `json:"head"`
}
