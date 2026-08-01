package main

type widgetPosition struct {
	x int
	y int
}

type workArea struct {
	left   int
	top    int
	right  int
	bottom int
}

type workAreaLookup func(hwnd uintptr) (left, top, right, bottom int)

func expandWidgetGeometry(win windowControl, dir string, lookup workAreaLookup) {
	var area workArea
	hasArea := false
	// Capture the monitor before widening the docked bar across a monitor boundary.
	if hwnd := win.NativeHandle(); hwnd != 0 && (dir == "right" || dir == "bottom") {
		left, top, right, bottom := lookup(hwnd)
		area = workArea{left: left, top: top, right: right, bottom: bottom}
		hasArea = true
	}
	win.SetSize(widgetWidth, widgetHeight)
	x, y := win.Position()
	position := widgetPosition{x: x, y: y}
	if hasArea || dir == "left" || dir == "top" {
		position = expandedWidgetPosition(dir, position, area)
	}
	win.SetPosition(position.x, position.y)
}

func expandedWidgetPosition(dir string, position widgetPosition, area workArea) widgetPosition {
	switch dir {
	case "left":
		position.x += snapEscapeStep
	case "top":
		position.y += snapEscapeStep
	case "right":
		position.x = area.right - widgetWidth - snapEscapeStep
	case "bottom":
		position.y = area.bottom - widgetHeight - snapEscapeStep
	}
	return position
}
