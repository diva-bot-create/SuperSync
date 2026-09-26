//go:build !darwin && !windows

package autostart

import "errors"

func Supported() bool   { return false }
func Enabled() bool     { return false }
func Set(on bool) error { return errors.New("opening at login isn't supported on this system") }
