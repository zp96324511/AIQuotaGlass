package main

import (
	"embed"
	"log"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/icons"

	"aiquotaglass/internal/config"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// The system drive may be full; allow relocating runtime data via env.
	dataDir := config.Dir()
	if v := os.Getenv("AQUOTA_DATA_DIR"); v != "" {
		dataDir = v
	}
	webviewDir := filepath.Join(dataDir, "webview")

	cfg, err := config.Load()
	if err != nil {
		log.Printf("config load: %v", err)
	}
	if cfg == nil {
		cfg = config.Default()
	}

	svc := &AppService{}

	app := application.New(application.Options{
		Name:        "AIQuotaGlass",
		Description: "多厂商 AI 套餐用量悬浮监视器",
		Services: []application.Service{
			application.NewService(svc),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Windows: application.WindowsOptions{
			WebviewUserDataPath:           webviewDir,
			DisableQuitOnLastWindowClosed: true,
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "AIQuotaGlass",
		Width:            widgetWidth,
		Height:           widgetHeight,
		Frameless:        true,
		AlwaysOnTop:      cfg.AlwaysOnTop,
		DisableResize:    true,
		BackgroundType:   application.BackgroundTypeTransparent,
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		Windows: application.WindowsWindow{
			DisableFramelessWindowDecorations: false,
			HiddenOnTaskbar:                   true,
		},
		URL: "/",
	})

	// Closing the widget hides it to the system tray instead of quitting.
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		win.Hide()
		e.Cancel()
	})

	svc.setup(app, &wailsWindow{app: app, win: win})

	setupTray(app, svc)

	err = app.Run()
	if err != nil {
		log.Fatal(err)
	}
}

// setupTray creates the system tray icon that keeps the app reachable while
// the widget is hidden: left click restores the window, the menu offers show,
// refresh and quit.
func setupTray(app *application.App, svc *AppService) {
	menu := application.NewMenu()
	showItem := menu.Add("显示窗口")
	showItem.OnClick(func(*application.Context) { svc.ShowMainWindow() })
	refreshItem := menu.Add("刷新数据")
	refreshItem.OnClick(func(*application.Context) { svc.RefreshAll() })
	menu.AddSeparator()
	quitItem := menu.Add("退出")
	quitItem.OnClick(func(*application.Context) { svc.Quit() })

	tray := app.SystemTray.New()
	tray.SetIcon(icons.SystrayLight)
	tray.SetDarkModeIcon(icons.SystrayDark)
	tray.SetTooltip("AIQuotaGlass")
	tray.SetMenu(menu)
	tray.OnClick(svc.ShowMainWindow)
	tray.Run()
}
