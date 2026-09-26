// Package window shows SuperSync in its own app window (WebKit on macOS,
// Edge WebView2 on Windows) instead of a browser tab.
package window

// Options describes the app window.
type Options struct {
	URL, Title    string
	Width, Height int
	// DataDir is where the web view keeps its own data (Windows).
	DataDir string
	// OnClose runs when the window is closing and the app is about to quit.
	OnClose func()
}
