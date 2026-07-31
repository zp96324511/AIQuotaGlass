//go:build windows

package main

import (
	"syscall"

	"aiquotaglass/internal/edge"
)

const vkLButton = 0x01

var procGetAsyncKeyState = syscall.NewLazyDLL("user32.dll").NewProc("GetAsyncKeyState")

// snap delegates to the platform edge-snap implementation.
func snap(hwnd uintptr, on bool) {
	edge.SnapToEdge(hwnd, on)
}

// mouseLeftDown reports whether the primary mouse button is currently held,
// used to avoid edge-snapping while the user is actively dragging the window.
func mouseLeftDown() bool {
	state, _, _ := procGetAsyncKeyState.Call(uintptr(vkLButton))
	return state&0x8000 != 0
}
