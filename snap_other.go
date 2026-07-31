//go:build !windows

package main

func snap(hwnd uintptr, on bool) {}

func mouseLeftDown() bool { return false }
