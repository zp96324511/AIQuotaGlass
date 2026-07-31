package main

import (
	"embed"
	"log"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"

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
			WebviewUserDataPath: webviewDir,
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "AIQuotaGlass",
		Width:            340,
		Height:           300,
		Frameless:        true,
		AlwaysOnTop:      cfg.AlwaysOnTop,
		DisableResize:    true,
		BackgroundType:   application.BackgroundTypeTransparent,
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		Windows: application.WindowsWindow{
			DisableFramelessWindowDecorations: false,
		},
		URL: "/",
	})

	svc.setup(app, &wailsWindow{app: app, win: win})

	err = app.Run()
	if err != nil {
		log.Fatal(err)
	}
}
