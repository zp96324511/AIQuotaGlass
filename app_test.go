package main

import "testing"

type geometryWindow struct {
	x, y   int
	width  int
	height int
	got    widgetPosition
}

func (w *geometryWindow) SetAlwaysOnTop(bool) {}
func (w *geometryWindow) SetPosition(x, y int) {
	w.x, w.y = x, y
	w.got = widgetPosition{x: x, y: y}
}
func (w *geometryWindow) Position() (int, int) { return w.x, w.y }
func (w *geometryWindow) SetSize(width, height int) {
	w.width, w.height = width, height
}
func (w *geometryWindow) Size() (int, int)        { return w.width, w.height }
func (w *geometryWindow) OnResize(func(w, h int)) {}
func (w *geometryWindow) Quit()                   {}
func (w *geometryWindow) Show()                   {}
func (w *geometryWindow) Hide()                   {}
func (w *geometryWindow) Focus()                  {}
func (w *geometryWindow) NativeHandle() uintptr   { return 1 }

func TestExpandWidgetGeometry_reads_work_area_before_resize(t *testing.T) {
	// Given a right-docked bar whose monitor changes if it is widened first.
	win := &geometryWindow{x: 1876, y: 200, width: snapBarHeight, height: snapBarWidth}
	lookup := func(uintptr) (int, int, int, int) {
		if win.width != snapBarHeight {
			return 1920, 0, 3840, 1040
		}
		return 0, 0, 1920, 1040
	}

	// When it expands.
	expandWidgetGeometry(win, "right", lookup, widgetWidth, widgetHeight)

	// Then it uses the work area captured while the bar was still on monitor 1.
	if got := win.got; got != (widgetPosition{x: 1540, y: 200}) {
		t.Fatalf("expanded position = %+v, want %+v", got, widgetPosition{x: 1540, y: 200})
	}
}

func TestExpandedWidgetPosition_keeps_widget_inside_original_work_area(t *testing.T) {
	tests := []struct {
		name     string
		dir      string
		position widgetPosition
		area     workArea
		want     widgetPosition
	}{
		{
			name:     "right edge of left monitor",
			dir:      "right",
			position: widgetPosition{x: 1876, y: 200},
			area:     workArea{left: 0, top: 0, right: 1920, bottom: 1040},
			want:     widgetPosition{x: 1540, y: 200},
		},
		{
			name:     "right edge of monitor with negative coordinates",
			dir:      "right",
			position: widgetPosition{x: -44, y: 200},
			area:     workArea{left: -1920, top: 0, right: 0, bottom: 1040},
			want:     widgetPosition{x: -380, y: 200},
		},
		{
			name:     "bottom edge",
			dir:      "bottom",
			position: widgetPosition{x: 200, y: 996},
			area:     workArea{left: 0, top: 0, right: 1920, bottom: 1040},
			want:     widgetPosition{x: 200, y: 700},
		},
		{
			name:     "left edge",
			dir:      "left",
			position: widgetPosition{x: 0, y: 200},
			want:     widgetPosition{x: 40, y: 200},
		},
		{
			name:     "top edge",
			dir:      "top",
			position: widgetPosition{x: 200, y: 0},
			want:     widgetPosition{x: 200, y: 40},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandedWidgetPosition(tt.dir, tt.position, tt.area, widgetWidth, widgetHeight)
			if got != tt.want {
				t.Fatalf("expanded position = %+v, want %+v", got, tt.want)
			}
		})
	}
}
