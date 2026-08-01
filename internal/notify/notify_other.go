//go:build !windows

package notify

import "context"

// noopNotifier is the non-Windows fallback: notifications are silently
// dropped because there is no Shell_NotifyIcon equivalent.
type noopNotifier struct{}

func newNotifier() Notifier { return &noopNotifier{} }

func (n *noopNotifier) Show(_ context.Context, _, _ string) error { return nil }
