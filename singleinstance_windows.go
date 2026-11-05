//go:build windows

package main

import (
	"log"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var instanceMutex windows.Handle

func acquireSingleInstance() bool {
	name, err := syscall.UTF16PtrFromString("Local\\NonoNotch")
	if err != nil {
		return true
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		if handle != 0 {
			windows.CloseHandle(handle)
		}
		return false
	}
	if err != nil {
		log.Printf("nono: single instance: %v", err)
		return true
	}
	instanceMutex = handle
	return true
}

func showMessage(text, title string) {
	proc := syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW")
	t, _ := syscall.UTF16PtrFromString(text)
	c, _ := syscall.UTF16PtrFromString(title)
	proc.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), 0x00000040)
}
