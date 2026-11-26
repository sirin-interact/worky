//go:build !windows

package watch

func newBackend(s sink) (backend, error) { return newNotifyBackend(s) }
