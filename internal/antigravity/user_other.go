//go:build !darwin

package antigravity

import "os/user"

func lookupOSUser(name string) (*user.User, error) { return user.Lookup(name) }
