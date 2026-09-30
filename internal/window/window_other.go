//go:build !darwin && !windows

package window

// Run reports false: there's no app window on this system (use a browser).
func Run(Options) bool { return false }

func Supported() bool       { return false }
func Focus()                {}
func Close()                {}
func SetKeepRunning(bool)   {}
func Edit(string)           {}
func Active() bool          { return false }
func Notify(string, string) {}
