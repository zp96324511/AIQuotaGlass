//go:build !windows

package main

func snap(hwnd uintptr, on bool) string { return "" }

func mouseLeftDown() bool { return false }
