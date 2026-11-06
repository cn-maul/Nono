package winfocus

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32dll                    = syscall.NewLazyDLL("user32.dll")
	kernel32dll                  = syscall.NewLazyDLL("kernel32.dll")
	procEnumWindows              = user32dll.NewProc("EnumWindows")
	procGetWindowThreadProcessID = user32dll.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible          = user32dll.NewProc("IsWindowVisible")
	procGetWindowRect            = user32dll.NewProc("GetWindowRect")
	procIsIconic                 = user32dll.NewProc("IsIconic")
	procShowWindow               = user32dll.NewProc("ShowWindow")
	procSetForegroundWindow      = user32dll.NewProc("SetForegroundWindow")
	procBringWindowToTop         = user32dll.NewProc("BringWindowToTop")
	procSetFocus                 = user32dll.NewProc("SetFocus")
	procGetForegroundWindow      = user32dll.NewProc("GetForegroundWindow")
	procAttachThreadInput        = user32dll.NewProc("AttachThreadInput")
	procKeybdEvent               = user32dll.NewProc("keybd_event")
	procGetWindowTextW           = user32dll.NewProc("GetWindowTextW")
	procGetCurrentThreadID       = kernel32dll.NewProc("GetCurrentThreadId")
)

type nativeRect struct {
	Left, Top, Right, Bottom int32
}

func windowTitle(hwnd uintptr) string {
	var title [256]uint16
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&title[0])), 256)
	return windows.UTF16ToString(title[:])
}

// Debug prints the top-level windows belonging to pids, or whose title
// contains hint.
func Debug(pids map[uint32]bool, hint string) {
	fmt.Printf("pids: %v\n", pids)
	callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var pid uint32
		procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		text := windowTitle(hwnd)
		if !pids[pid] && !strings.Contains(strings.ToLower(text), hint) {
			return 1
		}
		var rect nativeRect
		rectOK, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
		visible, _, _ := procIsWindowVisible.Call(hwnd)
		iconic, _, _ := procIsIconic.Call(hwnd)
		fmt.Printf("hwnd=%d pid=%d visible=%d iconic=%d rectOK=%d rect=%d,%d %dx%d title=%q\n",
			hwnd, pid, visible, iconic, rectOK,
			rect.Left, rect.Top, rect.Right-rect.Left, rect.Bottom-rect.Top, text)
		return 1
	})
	procEnumWindows.Call(callback, 0)
}

// HasTitle reports whether any window of pids has a title containing needle.
// Background helpers (DDE, IME) do not match, so this distinguishes a live app
// window from lingering Electron processes.
func HasTitle(pids map[uint32]bool, needle string) bool {
	if len(pids) == 0 {
		return false
	}
	needle = strings.ToLower(needle)
	found := false
	callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var pid uint32
		procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if !pids[pid] {
			return 1
		}
		if strings.Contains(strings.ToLower(windowTitle(hwnd)), needle) {
			found = true
			return 0
		}
		return 1
	})
	procEnumWindows.Call(callback, 0)
	return found
}

// Focus restores the best window of pids and brings it to the foreground.
// Windows' foreground lock is bypassed by attaching our thread to the current
// foreground thread, the usual desktop-app recipe. Windows owned by another
// process are only considered when their title hints at the app.
func Focus(pids map[uint32]bool, titleHints []string) error {
	var target uintptr
	var bestScore int64
	callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var pid uint32
		procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		text := strings.ToLower(windowTitle(hwnd))
		hinted := false
		for _, hint := range titleHints {
			if hint != "" && strings.Contains(text, hint) {
				hinted = true
				break
			}
		}
		if !pids[pid] && !hinted {
			return 1
		}
		var rect nativeRect
		if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect))); ok == 0 {
			return 1
		}
		area := int64(rect.Right-rect.Left) * int64(rect.Bottom-rect.Top)
		if area < 0 {
			area = 0
		}
		visible, _, _ := procIsWindowVisible.Call(hwnd)
		iconic, _, _ := procIsIconic.Call(hwnd)
		score := area
		if hinted {
			score += 1 << 50
		}
		if pids[pid] {
			score += 1 << 49
		}
		if visible != 0 {
			score += 1 << 40
		}
		if iconic != 0 {
			score += 1 << 30
		}
		if score > bestScore {
			bestScore = score
			target = hwnd
		}
		return 1
	})
	procEnumWindows.Call(callback, 0)
	if target == 0 {
		return errors.New("未找到应用窗口")
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if iconic, _, _ := procIsIconic.Call(target); iconic != 0 {
		procShowWindow.Call(target, 9) // SW_RESTORE
	}
	if visible, _, _ := procIsWindowVisible.Call(target); visible == 0 {
		procShowWindow.Call(target, 5) // SW_SHOW
	}

	currentResult, _, _ := procGetCurrentThreadID.Call()
	current := uint32(currentResult)
	foreground, _, _ := procGetForegroundWindow.Call()
	var foregroundPID uint32
	foregroundTID, _, _ := procGetWindowThreadProcessID.Call(foreground, uintptr(unsafe.Pointer(&foregroundPID)))
	foregroundThread := uint32(foregroundTID)
	var targetPID uint32
	targetTID, _, _ := procGetWindowThreadProcessID.Call(target, uintptr(unsafe.Pointer(&targetPID)))
	targetThread := uint32(targetTID)

	if foregroundThread != 0 && foregroundThread != current {
		procAttachThreadInput.Call(uintptr(current), uintptr(foregroundThread), 1)
		defer procAttachThreadInput.Call(uintptr(current), uintptr(foregroundThread), 0)
	}
	if targetThread != 0 && targetThread != current && targetThread != foregroundThread {
		procAttachThreadInput.Call(uintptr(current), uintptr(targetThread), 1)
		defer procAttachThreadInput.Call(uintptr(current), uintptr(targetThread), 0)
	}

	procShowWindow.Call(target, 5) // SW_SHOW
	procBringWindowToTop.Call(target)
	procSetForegroundWindow.Call(target)
	procSetFocus.Call(target)

	for i := 0; i < 5; i++ {
		if now, _, _ := procGetForegroundWindow.Call(); now == target {
			return nil
		}
		if i == 2 {
			// Last resort for Windows' foreground lock: a synthetic Alt key
			// press makes the next SetForegroundWindow call allowed.
			procKeybdEvent.Call(0x12, 0, 0, 0)
			procKeybdEvent.Call(0x12, 0, 2, 0)
		}
		procSetForegroundWindow.Call(target)
		time.Sleep(40 * time.Millisecond)
	}
	if now, _, _ := procGetForegroundWindow.Call(); now == target {
		return nil
	}
	return errors.New("无法把应用切到前台")
}
