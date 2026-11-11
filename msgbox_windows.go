//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")
)

// messageBox says something without a console, which a window program has
// none of: the reason it could not start, or what it found about the
// machine.
func messageBox(title, text string, isError bool) {
	t, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	b, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	const (
		mbIconError       = 0x00000010
		mbIconInformation = 0x00000040
		mbTopMost         = 0x00040000
	)
	flags := uintptr(mbIconInformation | mbTopMost)
	if isError {
		flags = mbIconError | mbTopMost
	}
	_, _, _ = procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(t)), flags)
}

// canLoad reports whether a system library is there at all, which is the
// first thing to check when a window never appears.
func canLoad(name string) bool {
	h, err := syscall.LoadLibrary(name)
	if err != nil {
		return false
	}
	_ = syscall.FreeLibrary(h)
	return true
}
