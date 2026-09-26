//go:build !windows

package web

func shellOpen(string) {}

func revealWindows(string) error { return nil }
