package main

import (
	"runtime"
	"time"
	"unsafe"
)

const vkLButton = 0x01

var (
	getCursorPos     = user32.NewProc("GetCursorPos")
	getWindowRect    = user32.NewProc("GetWindowRect")
	getAsyncKeyState = user32.NewProc("GetAsyncKeyState")
)

type mousePoint struct {
	X int32
	Y int32
}

type windowRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

// watchOutsideClick collapses the notch when the user clicks anywhere outside
// its window. The window never takes focus (WS_EX_NOACTIVATE), so there is no
// focus loss to listen for; polling the button state is the cheapest reliable
// signal. Runs until cancel closes.
func (n *Notch) watchOutsideClick(cancel <-chan struct{}) {
	// GetAsyncKeyState's "pressed since last call" bit is tracked per OS
	// thread, so keep this goroutine on one thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	down := leftButtonDown()
	for {
		select {
		case <-cancel:
			return
		case <-ticker.C:
		}
		state, _, _ := getAsyncKeyState.Call(vkLButton)
		// The low bit reports a press since the previous call, so fast taps
		// are not missed between ticks.
		pressed := state&0x0001 != 0 || (state&0x8000 != 0 && !down)
		down = state&0x8000 != 0
		if !pressed || n.pointerInside() {
			continue
		}
		n.Collapse()
		return
	}
}

func leftButtonDown() bool {
	state, _, _ := getAsyncKeyState.Call(vkLButton)
	return state&0x8000 != 0
}

func (n *Notch) pointerInside() bool {
	n.mu.Lock()
	win := n.window
	n.mu.Unlock()
	if win == nil {
		return true
	}
	var point mousePoint
	if ok, _, _ := getCursorPos.Call(uintptr(unsafe.Pointer(&point))); ok == 0 {
		return true
	}
	var rect windowRect
	if ok, _, _ := getWindowRect.Call(win.NativeHandle(), uintptr(unsafe.Pointer(&rect))); ok == 0 {
		return true
	}
	return point.X >= rect.Left && point.X < rect.Right &&
		point.Y >= rect.Top && point.Y < rect.Bottom
}
