package notify

import (
	"context"
	"log"
	"time"
)

// Notifier sends an OS-level notification for alerts.
type Notifier interface {
	Show(ctx context.Context, title, message string) error
}

var impl Notifier = newNotifier()

// BindHWNDProvider registers a function that returns the owner window
// handle. The HWND is resolved lazily on the first Show() call, so it is
// safe to call before the window has been created (e.g. before app.Run()).
// On platforms that don't need an HWND it is a no-op.
func BindHWNDProvider(provider func() uintptr) {
	if binder, ok := impl.(hwndBinder); ok {
		binder.bindHWNDProvider(provider)
	}
}

// hwndBinder is implemented by platform notifiers that need a window handle.
type hwndBinder interface {
	bindHWNDProvider(func() uintptr)
}

// ShowE dispatches a native notification and returns any error.
func ShowE(title, message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return impl.Show(ctx, title, message)
}

// Show dispatches a native notification, logging any error (fire-and-forget).
func Show(title, message string) {
	if err := ShowE(title, message); err != nil {
		log.Printf("notify: %v", err)
	}
}
