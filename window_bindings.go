package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"aiquotaglass/internal/edge"
	"aiquotaglass/internal/notify"
)

// OpenSettings opens the settings popup window, focusing it if already open.
// The window hosts the same frontend with the "?settings=1" view mode.
func (s *AppService) OpenSettings() {
	if s.settingsWin != nil {
		s.settingsWin.Show()
		s.settingsWin.Focus()
		return
	}
	w := s.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:                "settings",
		Title:               "AIQuotaGlass 设置",
		Width:               460,
		Height:              640,
		Frameless:           true,
		AlwaysOnTop:         true,
		DisableResize:       true,
		MinimiseButtonState: application.ButtonHidden,
		MaximiseButtonState: application.ButtonHidden,
		CloseButtonState:    application.ButtonHidden,
		BackgroundType:      application.BackgroundTypeSolid,
		BackgroundColour:    application.NewRGBA(24, 26, 36, 255),
		Windows:             application.WindowsWindow{DisableFramelessWindowDecorations: true},
		URL:                 "/?settings=1",
	})
	s.settingsWin = w
	w.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		s.settingsWin = nil
	})
}

// CloseSettings closes the settings popup window if it is open.
func (s *AppService) CloseSettings() {
	if s.settingsWin != nil {
		s.settingsWin.Close()
		s.settingsWin = nil
	}
}

// SnapIfNearEdge triggers one edge-snap pass (used on drag release).
func (s *AppService) SnapIfNearEdge() {
	if s.win != nil && s.win.NativeHandle() != 0 {
		s.snapMu.Lock()
		s.setSnapStateLocked(snap(s.win.NativeHandle(), true))
		s.snapMu.Unlock()
	}
}

// ExpandWidget restores the widget to its full size and pushes it away from
// the docked edge so the snap loop does not collapse it again immediately.
func (s *AppService) ExpandWidget() {
	s.snapMu.Lock()
	defer s.snapMu.Unlock()
	if s.win == nil {
		return
	}
	dir := s.snapped
	expandWidgetGeometry(s.win, dir, edge.WorkAreaForWindow, s.widgetW, s.widgetH)
	s.snapped = ""
	s.app.Event.Emit("widget:snap", map[string]any{"dir": "", "providerID": ""})
}

// ShowMainWindow restores and focuses the widget window (tray click / menu).
func (s *AppService) ShowMainWindow() {
	if s.win == nil {
		return
	}
	s.win.Show()
	s.win.Focus()
}

// HideToTray hides the widget window instead of quitting; it stays alive in
// the system tray and keeps refreshing in the background.
func (s *AppService) HideToTray() {
	if s.win == nil {
		return
	}
	s.win.Hide()
}

// Quit shuts the application down.
func (s *AppService) Quit() {
	if s.sched != nil {
		s.sched.Stop()
	}
	s.win.Quit()
}

// TestNotify fires a sample notification (settings preview).
// It always fires regardless of the NativeNotify toggle so users can verify
// the notification channel works before enabling automatic alerts.
func (s *AppService) TestNotify() error {
	return notify.ShowE("AIQuotaGlass", "告警通知测试")
}
