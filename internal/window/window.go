// Package window shows SuperSync in its own app window (WebKit on macOS,
// Edge WebView2 on Windows) instead of a browser tab.
package window

// Options describes the app window.
type Options struct {
	URL, Title    string
	Width, Height int
	// DataDir is where the web view keeps its own data (Windows).
	DataDir string
	// OnClose runs when the app is about to quit.
	OnClose func()
	// OnSyncAll is the menu bar / tray "Sync all playlists now".
	OnSyncAll func()
	// KeepRunning: closing the window hides it; SuperSync keeps running in
	// the menu bar (macOS) or notification area (Windows) until quit.
	KeepRunning bool
	// Hidden starts without showing the window (opened at login).
	Hidden bool
}
