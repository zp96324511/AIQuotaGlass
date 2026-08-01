//go:build windows

package edge

import (
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/w32"
)

const snapThreshold = 10 // pixels from screen edge that triggers snapping

const (
	spiGetWorkArea       = 0x0030
	monitorDefaultToNear = 0x00000002
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procSystemParameters = user32.NewProc("SystemParametersInfoW")
	procMonitorFrom      = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfo   = user32.NewProc("GetMonitorInfoW")
)

// monitorInfo matches the Windows MONITORINFO struct.
type monitorInfo struct {
	cbSize    uint32
	rcMonitor w32.RECT
	rcWork    w32.RECT
	dwFlags   uint32
}

// WorkArea returns the usable desktop rectangle (excludes taskbar).
func WorkArea() (left, top, right, bottom int) {
	var rc w32.RECT
	r, _, _ := procSystemParameters.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&rc)), 0)
	if r == 0 {
		return 0, 0, 1920, 1080
	}
	return int(rc.Left), int(rc.Top), int(rc.Right), int(rc.Bottom)
}

// workAreaForWindow returns the work area of the monitor the window is on,
// falling back to the primary monitor work area when the lookup fails.
func workAreaForWindow(hwnd uintptr) (left, top, right, bottom int) {
	hm, _, _ := procMonitorFrom.Call(hwnd, monitorDefaultToNear)
	if hm == 0 {
		return WorkArea()
	}
	var mi monitorInfo
	mi.cbSize = uint32(unsafe.Sizeof(mi))
	r, _, _ := procGetMonitorInfo.Call(hm, uintptr(unsafe.Pointer(&mi)))
	if r == 0 {
		return WorkArea()
	}
	return int(mi.rcWork.Left), int(mi.rcWork.Top), int(mi.rcWork.Right), int(mi.rcWork.Bottom)
}

// SnapToEdge snaps a window flush to the nearest edge of its own monitor
// when it is close enough and returns the direction it snapped to
// ("left"/"right"/"top"/"bottom", or "" when the window was not moved).
func SnapToEdge(hwnd uintptr, snap bool) string {
	if !snap {
		return ""
	}
	h := w32.HWND(hwnd)
	rect := w32.GetWindowRect(h)
	if rect == nil {
		return ""
	}
	w := int(rect.Right - rect.Left)
	hh := int(rect.Bottom - rect.Top)
	curX, curY := int(rect.Left), int(rect.Top)

	left, top, right, bottom := workAreaForWindow(hwnd)

	best := -1
	var targetX, targetY int
	dx, dy := 0, 0

	candidates := []struct {
		id int
		ok bool
		x  int
		y  int
	}{
		{0, abs(curX-left) <= snapThreshold, left, curY},
		{1, abs((right-w)-curX) <= snapThreshold, right - w, curY},
		{2, abs(curY-top) <= snapThreshold, curX, top},
		{3, abs((bottom-hh)-curY) <= snapThreshold, curX, bottom - hh},
	}
	for _, c := range candidates {
		if !c.ok {
			continue
		}
		d := abs(c.x-curX) + abs(c.y-curY)
		if best == -1 || d < dx+dy {
			best = c.id
			dx, dy = abs(c.x-curX), abs(c.y-curY)
			targetX, targetY = c.x, c.y
		}
	}
	if best == -1 {
		return ""
	}
	w32.SetWindowPos(h, 0, targetX, targetY, 0, 0,
		w32.SWP_NOSIZE|w32.SWP_NOZORDER|w32.SWP_NOACTIVATE|w32.SWP_NOSENDCHANGING)
	return []string{"left", "right", "top", "bottom"}[best]
}

// WindowBounds returns the current window position and size.
func WindowBounds(hwnd uintptr) (x, y, w, h int) {
	rect := w32.GetWindowRect(w32.HWND(hwnd))
	if rect == nil {
		return 0, 0, 0, 0
	}
	return int(rect.Left), int(rect.Top), int(rect.Right - rect.Left), int(rect.Bottom - rect.Top)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
