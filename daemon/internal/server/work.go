package server

import (
	"errors"
	"slices"

	"github.com/sirin-interact/worky/daemon/internal/files"
	"github.com/sirin-interact/worky/daemon/internal/project"
	"github.com/sirin-interact/worky/daemon/internal/protocol"
	"github.com/sirin-interact/worky/daemon/internal/search"
	"github.com/sirin-interact/worky/daemon/internal/watch"
)

// work is a request that runs beside the read loop, because git or a disk
// can take its time. then, when set, runs in the writer after the response
// has been written.
type work func() (result any, then func(), perr *protocol.Error)

// start runs a request beside the read loop. The read loop itself never
// waits: it is also what takes keystrokes and the answers to pings.
func (c *conn) start(id uint64, run work) {
	if c.waiting.Add(1) > maxWaiting {
		c.waiting.Add(-1)
		c.send(protocol.Response{ID: id, Error: protocol.Errorf(protocol.ErrBusy,
			"more than %d requests are waiting", maxWaiting)})
		return
	}
	go func() {
		select {
		case c.slots <- struct{}{}:
		case <-c.ctx.Done():
			c.waiting.Add(-1)
			return
		}
		result, then, perr := run()
		// The slot is kept until the response is on the wire, so a client
		// that does not read cannot make responses pile up.
		written := func() {
			<-c.slots
			c.waiting.Add(-1)
			if then != nil {
				then()
			}
		}
		if perr != nil {
			c.sendThen(protocol.Response{ID: id, Error: perr}, written)
			return
		}
		c.sendThen(protocol.Response{ID: id, Result: mustMarshal(result)}, written)
	}()
}

// answer makes work of a function from params to a result.
func answer[P, R any](req protocol.Request, fn func(P) (R, *protocol.Error)) work {
	return func() (any, func(), *protocol.Error) {
		params, perr := decode[P](req)
		if perr != nil {
			return nil, nil, perr
		}
		result, perr := fn(params)
		if perr != nil {
			return nil, nil, perr
		}
		return result, nil, nil
	}
}

// do makes work of a function that returns nothing but an error.
func do[P any](req protocol.Request, fn func(P) *protocol.Error) work {
	return answer(req, func(params P) (empty, *protocol.Error) { return empty{}, fn(params) })
}

// background returns the work a request asks for, or nil when the request is
// one that dispatch answers in order.
func (c *conn) background(req protocol.Request) work {
	projects, sessions := c.srv.Projects, c.srv.Sessions

	switch req.Method {
	case protocol.MethodProjectList:
		return answer(req, func(empty) (protocol.ProjectListResult, *protocol.Error) {
			return protocol.ProjectListResult{Projects: projects.List()}, nil
		})
	case protocol.MethodProjectAdd:
		return answer(req, func(p protocol.ProjectAddParams) (protocol.ProjectResult, *protocol.Error) {
			project, perr := projects.Add(p.Path)
			return protocol.ProjectResult{Project: project}, perr
		})
	case protocol.MethodProjectRename:
		return do(req, func(p protocol.ProjectRenameParams) *protocol.Error {
			return projects.Rename(p.ProjectID, p.Name)
		})
	case protocol.MethodProjectRemove:
		return do(req, func(p protocol.ProjectIDParams) *protocol.Error {
			return projects.Remove(p.ProjectID)
		})

	case protocol.MethodWorktreeCreate:
		return answer(req, func(p protocol.WorktreeCreateParams) (protocol.WorktreeResult, *protocol.Error) {
			worktree, perr := projects.CreateWorktree(p)
			return protocol.WorktreeResult{Worktree: worktree}, perr
		})
	case protocol.MethodWorktreeRemove:
		return do(req, func(p protocol.WorktreeRemoveParams) *protocol.Error {
			worktree, perr := projects.FindWorktree(p.ProjectID, p.Path)
			if perr != nil {
				return perr
			}
			if sessions.InWorktree(worktree.Path) {
				return protocol.Errorf(protocol.ErrInUse, "close the sessions in %q first", worktree.Path)
			}
			return projects.RemoveWorktree(p)
		})

	case protocol.MethodFsRoots:
		return answer(req, func(empty) (protocol.FsRootsResult, *protocol.Error) {
			return files.Roots(), nil
		})
	case protocol.MethodFsList:
		return answer(req, func(p protocol.FsPathParams) (protocol.FsListResult, *protocol.Error) {
			return files.List(p.Path)
		})
	case protocol.MethodFsStat:
		return answer(req, func(p protocol.FsPathParams) (protocol.FsStatResult, *protocol.Error) {
			return files.Stat(p.Path)
		})
	case protocol.MethodFsRead:
		return answer(req, files.Read)
	case protocol.MethodFsWrite:
		return answer(req, files.Write)
	case protocol.MethodFsMkdir:
		return do(req, func(p protocol.FsPathParams) *protocol.Error { return files.Mkdir(p.Path) })
	case protocol.MethodFsRename:
		return do(req, func(p protocol.FsRenameParams) *protocol.Error { return files.Rename(p.From, p.To) })
	case protocol.MethodFsRemove:
		return do(req, func(p protocol.FsRemoveParams) *protocol.Error { return files.Remove(p.Path, p.Recursive) })

	case protocol.MethodFsSearch:
		// A search ends with the connection that asked for it.
		return answer(req, func(p protocol.FsSearchParams) (protocol.FsSearchResult, *protocol.Error) {
			return search.Search(c.ctx, p)
		})

	case protocol.MethodGitStatus:
		return answer(req, func(p protocol.GitPathParams) (protocol.GitStatusResult, *protocol.Error) {
			return project.Status(p.Path)
		})
	case protocol.MethodGitStage:
		return do(req, func(p protocol.GitFilesParams) *protocol.Error { return project.Stage(p.Path, p.Files) })
	case protocol.MethodGitUnstage:
		return do(req, func(p protocol.GitFilesParams) *protocol.Error { return project.Unstage(p.Path, p.Files) })
	case protocol.MethodGitCommit:
		return answer(req, func(p protocol.GitCommitParams) (protocol.GitCommitResult, *protocol.Error) {
			head, perr := project.Commit(p.Path, p.Message)
			return protocol.GitCommitResult{Head: head}, perr
		})

	case protocol.MethodFsWatch:
		return func() (any, func(), *protocol.Error) {
			params, perr := decode[protocol.FsPathParams](req)
			if perr != nil {
				return nil, nil, perr
			}
			dw, perr := c.watchDir(params.Path)
			if perr != nil {
				return nil, nil, perr
			}
			// The client learns the watch's id from the response, so no
			// event for the watch may be sent before it.
			return protocol.FsWatchResult{Watch: dw.id}, func() { c.announce(dw) }, nil
		}
	case protocol.MethodFsUnwatch:
		return do(req, func(p protocol.FsUnwatchParams) *protocol.Error {
			c.mu.Lock()
			dw := c.watches[p.Watch]
			delete(c.watches, p.Watch)
			c.mu.Unlock()
			if dw == nil {
				return protocol.Errorf(protocol.ErrNotFound, "no such watch")
			}
			dw.watch.Stop()
			return nil
		})
	}
	return nil
}

// dirWatch is one directory a client watches. Its fields are guarded by the
// connection's mu.
type dirWatch struct {
	id    uint32
	watch *watch.Watch
	// announced is set once the client has been told the watch's id. Changes
	// seen before that are held.
	announced bool
	held      *watch.Change
}

func (c *conn) watchDir(path string) (*dirWatch, *protocol.Error) {
	path, perr := files.Clean(path)
	if perr != nil {
		return nil, perr
	}
	dw := &dirWatch{}
	// A directory in a worktree is watched through the worktree's own, which
	// is the one directory the daemon then keeps open.
	x, err := c.srv.Watcher.Add(path, c.srv.Projects.WorktreeOf(path), func(change watch.Change) { c.changed(dw, change) })
	if errors.Is(err, watch.ErrNotDir) {
		return nil, protocol.Errorf(protocol.ErrInvalidRequest, "%q is not a directory", path)
	}
	if err != nil {
		return nil, files.Error(err)
	}

	c.mu.Lock()
	over := c.watches == nil
	if !over {
		dw.id, dw.watch = c.nextWatch, x
		c.nextWatch++
		c.watches[dw.id] = dw
	}
	c.mu.Unlock()
	if over {
		x.Stop()
		return nil, protocol.Errorf(protocol.ErrInternal, "the connection is over")
	}
	return dw, nil
}

// changed passes a change in a watched directory on to the client.
func (c *conn) changed(dw *dirWatch, change watch.Change) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !dw.announced {
		dw.held = merge(dw.held, change)
		return
	}
	if c.watches[dw.id] == dw {
		c.sendChange(dw, change)
	}
}

// announce lets a watch's events through, starting with what was held back.
func (c *conn) announce(dw *dirWatch) {
	c.mu.Lock()
	defer c.mu.Unlock()
	dw.announced = true
	if dw.held != nil && c.watches[dw.id] == dw {
		c.sendChange(dw, *dw.held)
	}
	dw.held = nil
}

func (c *conn) sendChange(dw *dirWatch, change watch.Change) {
	c.send(protocol.Event{Event: protocol.EventFsChanged, Data: mustMarshal(protocol.FsChanged{
		Watch: dw.id, Names: change.Names, Ended: change.Ended,
	})})
}

// merge adds a change to the ones held so far.
func merge(held *watch.Change, change watch.Change) *watch.Change {
	if held == nil {
		return &change
	}
	held.Ended = held.Ended || change.Ended
	if held.Names == nil || change.Names == nil {
		held.Names = nil
		return held
	}
	for _, name := range change.Names {
		if !slices.Contains(held.Names, name) {
			held.Names = append(held.Names, name)
		}
	}
	slices.Sort(held.Names)
	return held
}

// unwatchAll ends every watch when the connection is over.
func (c *conn) unwatchAll() {
	c.mu.Lock()
	watches := c.watches
	c.watches = nil
	c.mu.Unlock()
	for _, dw := range watches {
		dw.watch.Stop()
	}
}
