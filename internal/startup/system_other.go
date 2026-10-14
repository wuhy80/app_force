//go:build !windows

package startup

const supported = false

func newSystemBackend(_ []string) (backend, error) { return nil, nil }
