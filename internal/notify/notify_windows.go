//go:build windows

package notify

import (
	"context"
	"sync"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/w32"
)

// windowsNotifier shows balloon tips via Shell_NotifyIconW using a hidden
// notification-area icon. No child process (powershell.exe) is spawned,
// no script file is written — the call is an in-process Win32 syscall,
// which eliminates the AV heuristic triggered by the old PowerShell bridge.
type windowsNotifier struct {
	mu   sync.Mutex
	hwnd uintptr // owner window (set via bindHWND from AppService.setup)
	uid  uint32  // notification icon identifier
	added bool   // whether NIM_ADD has been issued
}

func newNotifier() Notifier { return &windowsNotifier{uid: notifyUID} }

func (n *windowsNotifier) bindHWND(hwnd uintptr) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.hwnd = hwnd
}

func (n *windowsNotifier) Show(_ context.Context, title, message string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.hwnd == 0 {
		return errNoHWND
	}
	if !n.added {
		nid := buildNID(n.hwnd, n.uid, title, message)
		nid.UFlags = w32.NIF_ICON | w32.NIF_TIP | w32.NIF_INFO
		nid.HIcon = defaultIcon()
		nid.DwState = w32.NIS_HIDDEN // hide the persistent tray icon; balloon still shows
		if !w32.ShellNotifyIcon(w32.NIM_ADD, nid) {
			return errAddFailed
		}
		n.added = true
		return nil
	}
	nid := buildNID(n.hwnd, n.uid, title, message)
	nid.UFlags = w32.NIF_INFO
	if !w32.ShellNotifyIcon(w32.NIM_MODIFY, nid) {
		return errModifyFailed
	}
	return nil
}

// buildNID fills a NOTIFYICONDATA with balloon text. Title → SzInfoTitle,
// message → SzInfo, icon-flag set to NIIF_WARNING (amber) for threshold alerts.
func buildNID(hwnd uintptr, uid uint32, title, message string) *w32.NOTIFYICONDATA {
	nid := &w32.NOTIFYICONDATA{
		CbSize:       uint32(unsafe.Sizeof(w32.NOTIFYICONDATA{})),
		HWnd:         w32.HWND(hwnd),
		UID:          uid,
		UFlags:       w32.NIF_INFO,
		DwInfoFlags:  w32.NIIF_WARNING,
	}
	copyUTF16(nid.SzInfoTitle[:], title)
	copyUTF16(nid.SzInfo[:], message)
	return nid
}

// copyUTF16 is defined in copyutf16.go (platform-independent for testability).
