// Package files does what the protocol's fs methods ask, as the daemon's
// user.
package files

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sirin-interact/worky/daemon/internal/protocol"
)

// How much of a directory listing fits in one message, counted as the
// entries are written on the wire. Tests lower it.
var listBudget = 900 << 10

// Error turns a failed file operation into what a client is told.
func Error(err error) *protocol.Error {
	code := protocol.ErrIO
	switch {
	case errors.Is(err, fs.ErrNotExist):
		code = protocol.ErrNotFound
	case errors.Is(err, fs.ErrExist):
		code = protocol.ErrAlreadyExists
	case errors.Is(err, fs.ErrPermission):
		code = protocol.ErrPermissionDenied
	}
	return protocol.Errorf(code, "%v", err)
}

// Clean checks that path is absolute and returns it in the daemon's
// spelling.
func Clean(path string) (string, *protocol.Error) {
	if !filepath.IsAbs(path) {
		return "", protocol.Errorf(protocol.ErrInvalidRequest, "path %q is not absolute", path)
	}
	return filepath.Clean(path), nil
}

// Roots returns where browsing can start.
func Roots() protocol.FsRootsResult {
	home, _ := os.UserHomeDir()
	return protocol.FsRootsResult{Roots: roots(), Home: home}
}

// kind says what a path leads to, given what is at the path itself.
func kind(path string, mode fs.FileMode) (typ protocol.FileType, link bool) {
	switch {
	case mode.IsDir():
		return protocol.TypeDir, false
	case mode.IsRegular():
		return protocol.TypeFile, false
	}
	// A symbolic link, a Windows junction, or something that is neither a
	// file nor a directory. Following it tells which.
	target, err := os.Stat(path)
	switch {
	case err == nil && target.IsDir():
		return protocol.TypeDir, true
	case err == nil && target.Mode().IsRegular():
		return protocol.TypeFile, true
	}
	return protocol.TypeOther, mode&fs.ModeSymlink != 0
}

// version identifies a file's content as far as its size and time tell.
func version(info fs.FileInfo) string {
	return fmt.Sprintf("%x-%x", info.ModTime().UnixNano(), info.Size())
}

// List returns the entries of a directory, sorted by name.
func List(path string) (protocol.FsListResult, *protocol.Error) {
	path, perr := Clean(path)
	if perr != nil {
		return protocol.FsListResult{}, perr
	}
	dir, err := os.Open(path)
	if err != nil {
		return protocol.FsListResult{}, Error(err)
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil && len(entries) == 0 {
		if info, statErr := dir.Stat(); statErr == nil && !info.IsDir() {
			return protocol.FsListResult{}, protocol.Errorf(protocol.ErrInvalidRequest, "%q is not a directory", path)
		}
		return protocol.FsListResult{}, Error(err)
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })

	result := protocol.FsListResult{Path: path, Entries: make([]protocol.FsEntry, 0, len(entries))}
	budget := listBudget
	for _, entry := range entries {
		typ, link := kind(filepath.Join(path, entry.Name()), entry.Type())
		e := protocol.FsEntry{Name: entry.Name(), Type: typ, Link: link}
		if wire, err := json.Marshal(e); err == nil {
			budget -= len(wire) + 1
		}
		if budget < 0 {
			result.Truncated = true
			break
		}
		result.Entries = append(result.Entries, e)
	}
	return result, nil
}

// Stat describes what a path leads to.
func Stat(path string) (protocol.FsStatResult, *protocol.Error) {
	path, perr := Clean(path)
	if perr != nil {
		return protocol.FsStatResult{}, perr
	}
	self, err := os.Lstat(path)
	if err != nil {
		return protocol.FsStatResult{}, Error(err)
	}
	typ, link := kind(path, self.Mode())
	info := self
	if link {
		if target, err := os.Stat(path); err == nil {
			info = target
		}
	}
	return protocol.FsStatResult{
		Path:    path,
		Type:    typ,
		Link:    link,
		Size:    info.Size(),
		Mtime:   info.ModTime().UTC().Format(time.RFC3339Nano),
		Version: version(info),
	}, nil
}

// Read returns part of a file.
func Read(params protocol.FsReadParams) (protocol.FsReadResult, *protocol.Error) {
	path, perr := Clean(params.Path)
	if perr != nil {
		return protocol.FsReadResult{}, perr
	}
	length := int64(protocol.MaxFileChunk)
	if params.Length != nil {
		length = min(length, *params.Length)
	}
	if params.Offset < 0 || length < 0 {
		return protocol.FsReadResult{}, protocol.Errorf(protocol.ErrInvalidRequest, "offset and length cannot be negative")
	}

	file, err := os.Open(path)
	if err != nil {
		return protocol.FsReadResult{}, Error(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return protocol.FsReadResult{}, Error(err)
	}
	if !info.Mode().IsRegular() {
		return protocol.FsReadResult{}, protocol.Errorf(protocol.ErrInvalidRequest, "%q is not a file", path)
	}

	data := make([]byte, min(length, max(info.Size()-params.Offset, 0)))
	n, err := file.ReadAt(data, params.Offset)
	if err != nil && err != io.EOF {
		return protocol.FsReadResult{}, Error(err)
	}
	return protocol.FsReadResult{Data: data[:n], Size: info.Size(), Version: version(info)}, nil
}

// Write replaces a file's contents, or adds to them.
func Write(params protocol.FsWriteParams) (protocol.FsWriteResult, *protocol.Error) {
	path, perr := Clean(params.Path)
	if perr != nil {
		return protocol.FsWriteResult{}, perr
	}
	if len(params.Data) > protocol.MaxFileChunk {
		return protocol.FsWriteResult{}, protocol.Errorf(protocol.ErrInvalidRequest,
			"%d bytes is more than one call carries", len(params.Data))
	}
	if params.Exclusive && (params.Append || params.Expect != nil) {
		return protocol.FsWriteResult{}, protocol.Errorf(protocol.ErrInvalidRequest,
			"exclusive goes with neither append nor expect")
	}

	if params.Expect != nil {
		info, err := os.Stat(path)
		if err != nil {
			return protocol.FsWriteResult{}, Error(err)
		}
		if version(info) != *params.Expect {
			return protocol.FsWriteResult{}, protocol.Errorf(protocol.ErrConflict, "%q changed on disk", path)
		}
	}

	// Written into the file that is there, so its permissions, its links and
	// whatever watches it stay as they are.
	flags := os.O_WRONLY | os.O_CREATE
	switch {
	case params.Exclusive:
		flags |= os.O_EXCL
	case params.Append:
		flags |= os.O_APPEND
	default:
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(path, flags, 0o666)
	if err != nil {
		if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
			return protocol.FsWriteResult{}, protocol.Errorf(protocol.ErrInvalidRequest, "%q is a directory", path)
		}
		return protocol.FsWriteResult{}, Error(err)
	}
	_, err = file.Write(params.Data)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	// Asked once the file is closed: its time can still move until then.
	var info fs.FileInfo
	if err == nil {
		info, err = os.Stat(path)
	}
	if err != nil {
		return protocol.FsWriteResult{}, Error(err)
	}
	return protocol.FsWriteResult{Size: info.Size(), Version: version(info)}, nil
}

// Mkdir creates a directory and any parents it lacks.
func Mkdir(path string) *protocol.Error {
	path, perr := Clean(path)
	if perr != nil {
		return perr
	}
	if _, err := os.Lstat(path); err == nil {
		return protocol.Errorf(protocol.ErrAlreadyExists, "%q already exists", path)
	}
	if err := os.MkdirAll(path, 0o777); err != nil {
		return Error(err)
	}
	return nil
}

// Rename moves a file or a directory, unless something else is in the way.
func Rename(from, to string) *protocol.Error {
	from, perr := Clean(from)
	if perr != nil {
		return perr
	}
	if to, perr = Clean(to); perr != nil {
		return perr
	}
	source, err := os.Lstat(from)
	if err != nil {
		return Error(err)
	}
	// A name that differs only in case is the same file where case does not
	// count, and renaming to it is fine.
	if target, err := os.Lstat(to); err == nil && !os.SameFile(source, target) {
		return protocol.Errorf(protocol.ErrAlreadyExists, "%q already exists", to)
	}
	if err := os.Rename(from, to); err != nil {
		return Error(err)
	}
	return nil
}

// Remove deletes a file or a directory for good.
func Remove(path string, recursive bool) *protocol.Error {
	path, perr := Clean(path)
	if perr != nil {
		return perr
	}
	if filepath.Dir(path) == path {
		return protocol.Errorf(protocol.ErrInvalidRequest, "%q is the top of a file system", path)
	}
	if _, err := os.Lstat(path); err != nil {
		return Error(err)
	}
	remove := os.Remove
	if recursive {
		remove = os.RemoveAll
	}
	if err := remove(path); err != nil {
		return Error(err)
	}
	return nil
}
