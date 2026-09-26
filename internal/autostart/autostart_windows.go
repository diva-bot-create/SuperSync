package autostart

import (
	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func Supported() bool { return true }

// Enabled reports whether SuperSync opens at login.
func Enabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue("SuperSync")
	return err == nil
}

// Set turns opening at login on or off. It's also called at every start
// while on, so the login item follows the app if it's moved.
func Set(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue("SuperSync"); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := executable()
	if err != nil {
		return err
	}
	return k.SetStringValue("SuperSync", `"`+exe+`" `+Arg)
}
