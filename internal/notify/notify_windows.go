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
	mu       sync.Mutex
	hwndFunc func() uintptr // owner window provider (set via bindHWNDProvider)
	uid      uint32         // notification icon identifier
	added    bool           // whether NIM_ADD has been issued
}

func newNotifier() Notifier { return &windowsNotifier{uid: notifyUID} }

func (n *windowsNotifier) bindHWNDProvider(f func() uintptr) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.hwndFunc = f
}

func (n *windowsNotifier) Show(_ context.Context, title, message string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	hwnd := uintptr(0)
	if n.hwndFunc != nil {
		hwnd = n.hwndFunc()
	}
	if hwnd == 0 {
		return errNoHWND
	}
	if !n.added {
		nid := buildNID(hwnd, n.uid, title, message)
		nid.UFlags = w32.NIF_ICON | w32.NIF_TIP | w32.NIF_INFO | w32.NIF_STATE
		nid.HIcon = defaultIcon()
		nid.DwState = w32.NIS_HIDDEN
		nid.DwStateMask = w32.NIS_HIDDEN
		if !w32.ShellNotifyIcon(w32.NIM_ADD, nid) {
			return errAddFailed
		}
		// Some Windows versions ignore NIF_STATE on NIM_ADD and only hide
		// the icon via a follow-up NIM_MODIFY — without it a second
		// (default-icon) tray icon would be visible.
		hide := buildNID(hwnd, n.uid, title, message)
		hide.UFlags = w32.NIF_STATE
		hide.DwState = w32.NIS_HIDDEN
		hide.DwStateMask = w32.NIS_HIDDEN
		w32.ShellNotifyIcon(w32.NIM_MODIFY, hide)
		n.added = true
		return nil
	}
	nid := buildNID(hwnd, n.uid, title, message)
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
