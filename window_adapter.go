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

func (w *wailsWindow) SetSize(width, height int) { w.win.SetSize(width, height) }

func (w *wailsWindow) Quit() { w.app.Quit() }

func (w *wailsWindow) Show() { w.win.Show() }

func (w *wailsWindow) Hide() { w.win.Hide() }

func (w *wailsWindow) Focus() { w.win.Focus() }

func (w *wailsWindow) NativeHandle() uintptr {
	return uintptr(w.win.NativeWindow())
}

var _ windowControl = (*wailsWindow)(nil)
