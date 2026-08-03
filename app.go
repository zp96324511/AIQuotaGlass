package main

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"aiquotaglass/internal/config"
	"aiquotaglass/internal/edge"
	"aiquotaglass/internal/providers"
	"aiquotaglass/internal/scheduler"
)

// windowControl is the subset of the Wails window API the service uses,
// isolated so the service stays testable on any platform.
type windowControl interface {
	SetAlwaysOnTop(bool)
	SetPosition(x, y int)
	Position() (x, y int)
	SetSize(w, h int)
	Quit()
	Show()
	Hide()
	Focus()
	NativeHandle() uintptr
}

// Widget geometry. When docked to a screen edge the widget collapses into a
// slim bar: three stacked quota bars (left/right) or three side-by-side quota
// bars (top/bottom).
const (
	widgetWidth    = 340 // full-size widget width
	widgetHeight   = 300 // full-size widget height
	snapBarWidth   = 150 // bar length (vertical bar height / horizontal bar width)
	snapBarHeight  = 44  // bar thickness
	snapEscapeStep = 40  // pixels pushed away from the edge on expand
)

// AppService is the backend surface exposed to the frontend via Wails bindings.
type AppService struct {
	app   *application.App
	win   windowControl
	cfg   *config.AppConfig
	sched *scheduler.Scheduler

	settingsWin *application.WebviewWindow // lazily created settings popup

	mu         sync.Mutex
	lastStatus []providers.Result
	alertArmed map[string]bool // providerID/windowKey -> currently above threshold

	refreshMu        sync.Mutex
	refreshRound     uint64
	configVersion    uint64
	quotaSnapshots   map[string]quotaSnapshot
	lastChangedRound map[string]uint64

	snapMu  sync.Mutex // serializes snap state with native window geometry changes
	snapped string     // current edge the widget is docked to ("" = full size)
}

// setup wires the service to the running application.
func (s *AppService) setup(app *application.App, win windowControl) {
	s.app = app
	s.win = win
	s.alertArmed = map[string]bool{}

	cfg, err := config.Load()
	if err != nil {
		log.Printf("config: %v", err)
	}
	if cfg == nil {
		cfg = config.Default()
	}
	s.cfg = cloneAppConfig(cfg)
	s.quotaSnapshots = map[string]quotaSnapshot{}
	s.lastChangedRound = map[string]uint64{}
	s.applyWindowState()

	interval := time.Duration(cfg.RefreshIntervalSec) * time.Second
	s.sched = scheduler.New(interval, s.refresh)
	s.sched.Start()

	go s.edgeDockLoop()
}

func (s *AppService) applyWindowState() {
	if s.win == nil {
		return
	}
	s.mu.Lock()
	on := s.cfg != nil && s.cfg.AlwaysOnTop
	s.mu.Unlock()
	s.win.SetAlwaysOnTop(on)
}

// edgeDockLoop continuously snaps the window to screen edges when enabled.
// Snapping is skipped while the user is actively dragging (left button held).
// When docked, the widget collapses into a slim progress bar (see setSnapState).
func (s *AppService) edgeDockLoop() {
	t := time.NewTicker(800 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		cfg := s.configSnapshot()
		if cfg == nil || !cfg.EdgeDock || s.win == nil {
			continue
		}
		if s.win.NativeHandle() != 0 && !mouseLeftDown() {
			s.snapMu.Lock()
			s.setSnapStateLocked(snap(s.win.NativeHandle(), true))
			s.snapMu.Unlock()
		}
	}
}

// setSnapStateLocked transitions the widget while snapMu is held.
func (s *AppService) setSnapStateLocked(dir string) {
	if dir == s.snapped || s.win == nil {
		return
	}
	s.snapped = dir
	if dir == "" {
		s.win.SetSize(widgetWidth, widgetHeight)
		s.app.Event.Emit("widget:snap", map[string]any{"dir": "", "providerID": ""})
		return
	}
	var barW, barH int
	if dir == "left" || dir == "right" {
		barW, barH = snapBarHeight, snapBarWidth // vertical bar
	} else {
		barW, barH = snapBarWidth, snapBarHeight // horizontal bar
	}
	s.win.SetSize(barW, barH)
	// SetSize keeps the top-left corner, so right/bottom-docked bars must be
	// pushed flush with the edge. Win32 SetWindowPos (edge.ReAnchor) is silently
	// overridden by Wails v3's window manager, so reposition via the Wails API.
	if dir == "right" || dir == "bottom" {
		if hwnd := s.win.NativeHandle(); hwnd != 0 {
			_, _, right, bottom := edge.WorkAreaForWindow(hwnd)
			x, y := s.win.Position()
			if dir == "right" {
				x = right - barW
			} else {
				y = bottom - barH
			}
			s.win.SetPosition(x, y)
		}
	}
	s.app.Event.Emit("widget:snap", map[string]any{"dir": dir, "providerID": s.snapProviderID()})
}

// snapProviderID returns the account whose quota the edge bar shows. When no
// account is explicitly chosen it falls back to the first one in list order
// (which follows the user's SortOrder).
func (s *AppService) snapProviderID() string {
	cfg := s.configSnapshot()
	if cfg == nil {
		return ""
	}
	if cfg.SnapProviderID != "" {
		return cfg.SnapProviderID
	}
	if len(cfg.Providers) > 0 {
		return cfg.Providers[0].ID
	}
	return ""
}

// ---- Bindings exposed to the frontend ----

// GetConfig returns the current configuration.
func (s *AppService) GetConfig() *config.AppConfig {
	return s.configSnapshot()
}

// GetProviderTypes returns the provider types that have a coded adapter
// registered, for the settings UI to enumerate when adding an account.
func (s *AppService) GetProviderTypes() []providers.ProviderType {
	return providers.Types()
}

// SaveConfig persists the configuration and re-applies runtime settings.
func (s *AppService) SaveConfig(cfg *config.AppConfig) error {
	if cfg == nil {
		cfg = config.Default()
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if err := config.Save(cfg); err != nil {
		return err
	}
	s.mu.Lock()
	s.configVersion++
	s.cfg = cloneAppConfig(cfg)
	s.lastStatus = nil
	s.quotaSnapshots = map[string]quotaSnapshot{}
	s.lastChangedRound = map[string]uint64{}
	savedCfg := cloneAppConfig(s.cfg)
	configVersion := s.configVersion
	barrierRoundID := s.refreshRound
	s.mu.Unlock()
	s.applyWindowState()
	if s.sched != nil {
		interval := time.Duration(cfg.RefreshIntervalSec) * time.Second
		if interval <= 0 {
			interval = 300 * time.Second
		}
		s.sched.SetInterval(interval)
	}
	s.app.Event.Emit("config:saved", configSavedEvent{
		Version: configVersion,
		RoundID: barrierRoundID,
		Config:  savedCfg,
	})
	go s.refresh(context.Background())
	return nil
}

// RefreshAll forces an immediate refresh of every enabled provider.
func (s *AppService) RefreshAll() []providers.Result {
	s.sched.RunNow()
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]providers.Result(nil), s.lastStatus...)
}

// SetAlwaysOnTop toggles window pinning.
func (s *AppService) SetAlwaysOnTop(on bool) {
	s.mu.Lock()
	if s.cfg != nil {
		s.cfg.AlwaysOnTop = on
	}
	s.mu.Unlock()
	s.win.SetAlwaysOnTop(on)
}

func formatPercent(f float64) string {
	return strconv.FormatFloat(f, 'f', 1, 64) + "%"
}
