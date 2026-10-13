//go:build desktop && !windows

package main

import "context"

func startDesktopTray(*Desktop) func()                  { return func() {} }
func desktopBeforeClose(*Desktop, context.Context) bool { return false }
