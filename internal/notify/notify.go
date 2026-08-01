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

var impl Notifier = newWindowsNotifier()

// ShowE dispatches a native notification and returns any error.
func ShowE(title, message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return impl.Show(ctx, title, message)
}

// Show dispatches a native notification, logging any error (fire-and-forget).
func Show(title, message string) {
	if err := ShowE(title, message); err != nil {
		log.Printf("notify: %v", err)
	}
}
