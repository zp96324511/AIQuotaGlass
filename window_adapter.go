package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
)

// wailsWindow adapts the Wails webview window to the windowControl interface
// the AppService depends on, isolating the Wails API from business logic.
type wailsWindow struct {
	app *application.App
	win *application.WebviewWindow
}

func (w *wailsWindow) SetAlwaysOnTop(on bool) { w.win.SetAlwaysOnTop(on) }

func (w *wailsWindow) SetPosition(x, y int) { w.win.SetPosition(x, y) }

func (w *wailsWindow) Position() (int, int) { return w.win.Position() }

func (w *wailsWindow) Quit() { w.app.Quit() }

func (w *wailsWindow) NativeHandle() uintptr {
	return uintptr(w.win.NativeWindow())
}

var _ windowControl = (*wailsWindow)(nil)
