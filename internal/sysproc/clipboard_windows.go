//go:build windows

package sysproc

import (
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

var (
	modUser32Clip        = syscall.NewLazyDLL("user32.dll")
	procOpenClipboard    = modUser32Clip.NewProc("OpenClipboard")
	procCloseClipboard   = modUser32Clip.NewProc("CloseClipboard")
	procEmptyClipboard   = modUser32Clip.NewProc("EmptyClipboard")
	procSetClipboardData = modUser32Clip.NewProc("SetClipboardData")

	modKernel32Clip   = syscall.NewLazyDLL("kernel32.dll")
	procGlobalAlloc   = modKernel32Clip.NewProc("GlobalAlloc")
	procGlobalFree    = modKernel32Clip.NewProc("GlobalFree")
	procGlobalLock    = modKernel32Clip.NewProc("GlobalLock")
	procGlobalUnlock  = modKernel32Clip.NewProc("GlobalUnlock")
	procRtlMoveMemory = modKernel32Clip.NewProc("RtlMoveMemory")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

// CopyTextToClipboard schreibt Unicode-Text direkt über die Win32-API (CF_UNICODETEXT).
// clip.exe hat UTF-8 als OEM-Codepage gelesen und Umlaute zerstört.
func CopyTextToClipboard(text string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	u16, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}

	// Die Zwischenablage kann kurz von einem anderen Programm belegt sein.
	var opened bool
	for i := 0; i < 10; i++ {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			opened = true
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !opened {
		return fmt.Errorf("Zwischenablage ist belegt")
	}
	defer procCloseClipboard.Call()

	if r, _, e := procEmptyClipboard.Call(); r == 0 {
		return fmt.Errorf("EmptyClipboard: %v", e)
	}

	size := uintptr(len(u16) * 2)
	hMem, _, e := procGlobalAlloc.Call(gmemMoveable, size)
	if hMem == 0 {
		return fmt.Errorf("GlobalAlloc: %v", e)
	}
	ptr, _, e := procGlobalLock.Call(hMem)
	if ptr == 0 {
		procGlobalFree.Call(hMem)
		return fmt.Errorf("GlobalLock: %v", e)
	}
	procRtlMoveMemory.Call(ptr, uintptr(unsafe.Pointer(&u16[0])), size)
	procGlobalUnlock.Call(hMem)

	if r, _, e := procSetClipboardData.Call(cfUnicodeText, hMem); r == 0 {
		procGlobalFree.Call(hMem)
		return fmt.Errorf("SetClipboardData: %v", e)
	}
	// Nach erfolgreichem SetClipboardData gehört hMem dem System, nicht freigeben.
	return nil
}
