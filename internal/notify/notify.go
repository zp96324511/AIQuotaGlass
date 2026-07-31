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

// Show dispatches a native notification.
func Show(title, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := impl.Show(ctx, title, message); err != nil {
		log.Printf("notify: %v", err)
	}
}
