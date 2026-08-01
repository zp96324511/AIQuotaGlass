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

// BindHWND attaches the owner window handle so platform implementations
// that need it (e.g. Shell_NotifyIconW) can register their notification
// icon. It is safe to call before the first Show; on platforms that don't
// need an HWND it is a no-op.
func BindHWND(hwnd uintptr) {
	if binder, ok := impl.(hwndBinder); ok {
		binder.bindHWND(hwnd)
	}
}

// hwndBinder is implemented by platform notifiers that need a window handle.
type hwndBinder interface {
	bindHWND(uintptr)
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
