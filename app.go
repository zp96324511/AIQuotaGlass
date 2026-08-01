package main

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"aiquotaglass/internal/config"
	"aiquotaglass/internal/notify"
	"aiquotaglass/internal/providers"
	"aiquotaglass/internal/scheduler"
)

// windowControl is the subset of the Wails window API the service uses,
// isolated so the service stays testable on any platform.
type windowControl interface {
	SetAlwaysOnTop(bool)
	SetPosition(x, y int)
	Position() (x, y int)
	Quit()
	Show()
	Hide()
	Focus()
	NativeHandle() uintptr
}

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
	s.cfg = cfg
	if s.cfg == nil {
		s.cfg = config.Default()
	}
	s.applyWindowState()

	interval := time.Duration(s.cfg.RefreshIntervalSec) * time.Second
	s.sched = scheduler.New(interval, s.refresh)
	s.sched.Start()

	go s.edgeDockLoop()
}

func (s *AppService) refresh(ctx context.Context) {
	results := make([]providers.Result, 0, len(s.cfg.Providers))
	for i := range s.cfg.Providers {
		pc := s.cfg.Providers[i]
		if !pc.Enabled {
			continue
		}
		p, err := providers.New(pc)
		if err != nil {
			continue
		}
		res, err := p.Query(ctx)
		if err != nil {
			log.Printf("query %s: %v", pc.ID, err)
		}
		if res != nil {
			results = append(results, *res)
			s.checkAlerts(res)
		}
	}

	s.mu.Lock()
	s.lastStatus = results
	s.mu.Unlock()
	s.app.Event.Emit("usage:update", results)
}

func (s *AppService) checkAlerts(res *providers.Result) {
	if res.Error != "" || s.cfg == nil {
		return
	}
	cfg := config.Get()
	for i := range res.Windows {
		w := res.Windows[i]
		p := cfg.ProviderConfig(res.ProviderID)
		threshold, ok := p.AlertThresholds[w.Key]
		if !ok {
			continue
		}
		key := res.ProviderID + "/" + w.Key
		above := w.Percent >= float64(threshold)
		if above && !s.alertArmed[key] {
			s.alertArmed[key] = true
			msg := alertMessage(p.Name, w, threshold)
			s.app.Event.Emit("usage:alert", map[string]any{
				"provider": p.Name, "window": w.Label, "percent": w.Percent, "threshold": threshold,
			})
			if cfg.NativeNotify {
				notify.Show("AIQuotaGlass 用量告警", msg)
			}
		} else if !above {
			s.alertArmed[key] = false
		}
	}
}

func alertMessage(name string, w providers.WindowStatus, threshold int) string {
	return name + " " + w.Label + " 用量已达 " + formatPercent(w.Percent) + " (阈值 " + strconv.Itoa(threshold) + "%)"
}

func (s *AppService) applyWindowState() {
	if s.win == nil {
		return
	}
	s.win.SetAlwaysOnTop(s.cfg.AlwaysOnTop)
}

// edgeDockLoop continuously snaps the window to screen edges when enabled.
// Snapping is skipped while the user is actively dragging (left button held).
func (s *AppService) edgeDockLoop() {
	t := time.NewTicker(800 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		cfg := config.Get()
		if cfg == nil || !cfg.EdgeDock || s.win == nil {
			continue
		}
		if s.win.NativeHandle() != 0 && !mouseLeftDown() {
			snap(s.win.NativeHandle(), true)
		}
	}
}

// ---- Bindings exposed to the frontend ----

// GetConfig returns the current configuration.
func (s *AppService) GetConfig() *config.AppConfig {
	cfg, _ := config.Load()
	if cfg == nil {
		cfg = config.Default()
	}
	return cfg
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
	if err := config.Save(cfg); err != nil {
		return err
	}
	s.cfg = cfg
	s.applyWindowState()
	if s.sched != nil {
		interval := time.Duration(cfg.RefreshIntervalSec) * time.Second
		if interval <= 0 {
			interval = 300 * time.Second
		}
		s.sched.SetInterval(interval)
	}
	go s.refresh(context.Background())
	s.app.Event.Emit("config:saved", cfg)
	return nil
}

// RefreshAll forces an immediate refresh of every enabled provider.
func (s *AppService) RefreshAll() []providers.Result {
	s.sched.RunNow()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastStatus
}

// SetAlwaysOnTop toggles window pinning.
func (s *AppService) SetAlwaysOnTop(on bool) {
	s.cfg.AlwaysOnTop = on
	s.win.SetAlwaysOnTop(on)
}

// OpenSettings opens the settings popup window, focusing it if already open.
// The window hosts the same frontend with the "?settings=1" view mode.
func (s *AppService) OpenSettings() {
	if s.settingsWin != nil {
		s.settingsWin.Show()
		s.settingsWin.Focus()
		return
	}
	w := s.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "settings",
		Title:            "AIQuotaGlass 设置",
		Width:            460,
		Height:           640,
		Frameless:        true,
		AlwaysOnTop:      true,
		DisableResize:    true,
		BackgroundType:   application.BackgroundTypeSolid,
		BackgroundColour: application.NewRGBA(24, 26, 36, 255),
		Windows:          application.WindowsWindow{DisableFramelessWindowDecorations: true},
		URL:              "/?settings=1",
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
		snap(s.win.NativeHandle(), true)
	}
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
func (s *AppService) TestNotify() {
	cfg := config.Get()
	if cfg != nil && cfg.NativeNotify {
		notify.Show("AIQuotaGlass", "告警通知测试")
	}
}

func formatPercent(f float64) string {
	return strconv.FormatFloat(f, 'f', 1, 64) + "%"
}
