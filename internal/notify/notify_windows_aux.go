//go:build windows

package notify

import (
	"errors"

	"github.com/wailsapp/wails/v3/pkg/w32"
)

// notifyUID provides a unique notification-area icon identifier.
// Using a non-zero value avoids collision with the Wails tray icon (uid=parent.id).
const notifyUID uint32 = 0xA1A1

// defaultIcon returns a system default application icon for the balloon.
// We avoid loading a custom icon here to keep the notification path
// dependency-free; the balloon still renders correctly with IDI_APPLICATION.
func defaultIcon() w32.HICON {
	return w32.LoadIconWithResourceID(0, uint16(w32.IDI_APPLICATION))
}


var (
	errNoHWND       = errors.New("notify: window handle not bound")
	errAddFailed    = errors.New("notify: Shell_NotifyIcon NIM_ADD failed")
	errModifyFailed = errors.New("notify: Shell_NotifyIcon NIM_MODIFY failed")
)
