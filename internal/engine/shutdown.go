package engine

import (
	"errors"
	"net"
	"os"
)

// The core closes sockets during cancellation and again during cleanup. Treat
// already-closed resources as success, but never hide another cleanup failure
// joined to a closed-resource error (including nested sing-box error wrappers).
func isAlreadyClosedError(err error) bool {
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		children := wrapped.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !isAlreadyClosedError(child) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return isAlreadyClosedError(wrapped.Unwrap())
	default:
		return errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed)
	}
}
