package qoder

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

// FocusMainWindow restores the largest visible Qoder window and brings it
// to the foreground. Windows' foreground lock is bypassed by attaching our
// thread to the current foreground thread, the usual desktop-app recipe.
// DebugWindows prints the top-level Qoder windows. It backs `nono windows`.
func DebugWindows() {
	pids := qoderPIDs()
	fmt.Printf("qoder pids: %v\n", pids)
	callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var pid uint32
		procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		var title [256]uint16
		procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&title[0])), 256)
		text := windows.UTF16ToString(title[:])
		if !pids[pid] && !strings.Contains(strings.ToLower(text), "qoder") {
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

func FocusMainWindow() error {
	pids := qoderPIDs()
	if len(pids) == 0 {
		return errors.New("Qoder 未运行")
	}
	var target uintptr
	var bestScore int64
	callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var pid uint32
		procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if !pids[pid] {
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
		var title [256]uint16
		procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&title[0])), 256)
		text := strings.ToLower(windows.UTF16ToString(title[:]))
		visible, _, _ := procIsWindowVisible.Call(hwnd)
		iconic, _, _ := procIsIconic.Call(hwnd)
		score := area
		if strings.Contains(text, "qoder") {
			score += 1 << 50
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
		return errors.New("未找到 Qoder 窗口")
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
	return errors.New("无法把 Qoder 切到前台")
}
