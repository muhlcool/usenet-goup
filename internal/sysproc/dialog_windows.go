//go:build windows

package sysproc

import (
	"runtime"
	"syscall"
	"unsafe"
)

var (
	modUser32               = syscall.NewLazyDLL("user32.dll")
	procCreateWindowExW     = modUser32.NewProc("CreateWindowExW")
	procDestroyWindow       = modUser32.NewProc("DestroyWindow")
	procSetForegroundWindow = modUser32.NewProc("SetForegroundWindow")
	procBringWindowToTop    = modUser32.NewProc("BringWindowToTop")
	procGetSystemMetrics    = modUser32.NewProc("GetSystemMetrics")

	modComdlg32          = syscall.NewLazyDLL("comdlg32.dll")
	procGetOpenFileNameW = modComdlg32.NewProc("GetOpenFileNameW")

	modShell32              = syscall.NewLazyDLL("shell32.dll")
	procSHBrowseForFolderW  = modShell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDList = modShell32.NewProc("SHGetPathFromIDListW")

	modOle32          = syscall.NewLazyDLL("ole32.dll")
	procCoTaskMemFree = modOle32.NewProc("CoTaskMemFree")
	procCoInitialize  = modOle32.NewProc("CoInitializeEx")
	procCoUninit      = modOle32.NewProc("CoUninitialize")
)

type openFileNameW struct {
	lStructSize       uint32
	hwndOwner         uintptr
	hInstance         uintptr
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
	pvReserved        unsafe.Pointer
	dwReserved        uint32
	flagsEx           uint32
}

type browseInfoW struct {
	hwndOwner      uintptr
	pidlRoot       uintptr
	pszDisplayName *uint16
	lpszTitle      *uint16
	ulFlags        uint32
	lpfn           uintptr
	lParam         uintptr
	iImage         int32
}

func createTopmostOwner() uintptr {
	className, _ := syscall.UTF16PtrFromString("STATIC")
	winTitle, _ := syscall.UTF16PtrFromString("")
	screenW, _, _ := procGetSystemMetrics.Call(0)
	screenH, _, _ := procGetSystemMetrics.Call(1)
	x := int32(screenW) / 2
	y := int32(screenH) / 2

	const (
		wsExTopmost    = 0x00000008
		wsExToolWindow = 0x00000080
		wsPopup        = 0x80000000
	)
	hwnd, _, _ := procCreateWindowExW.Call(
		wsExTopmost|wsExToolWindow,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(winTitle)),
		wsPopup,
		uintptr(x), uintptr(y), 0, 0,
		0, 0, 0, 0,
	)
	if hwnd != 0 {
		_, _, _ = procBringWindowToTop.Call(hwnd)
		_, _, _ = procSetForegroundWindow.Call(hwnd)
	}
	return hwnd
}

func OpenDialog(mode string) (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	owner := createTopmostOwner()
	if owner != 0 {
		defer procDestroyWindow.Call(owner)
	}

	isFolder := mode == "folder" || mode == "nzbdir"
	if isFolder {
		// S_OK (0) oder S_FALSE (1) muss mit CoUninitialize ausgeglichen werden.
		if hr, _, _ := procCoInitialize.Call(0, 0x2); hr == 0 || hr == 1 {
			defer procCoUninit.Call()
		}
		titlePtr, _ := syscall.UTF16PtrFromString("Ordner auswählen")
		dispBuf := make([]uint16, 260)
		bi := browseInfoW{
			hwndOwner:      owner,
			pszDisplayName: &dispBuf[0],
			lpszTitle:      titlePtr,
			ulFlags:        0x0001 | 0x0040,
		}
		pidl, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
		if pidl == 0 {
			return "", nil
		}
		defer procCoTaskMemFree.Call(pidl)

		pathBuf := make([]uint16, 32768)
		ret, _, _ := procSHGetPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&pathBuf[0])))
		if ret == 0 {
			return "", nil
		}
		return DisplayPath(syscall.UTF16ToString(pathBuf)), nil
	}

	fileBuf := make([]uint16, 32768)
	titleStr := "Datei auswählen"
	filterSlice := []uint16{'A', 'l', 'l', 'e', ' ', 'D', 'a', 't', 'e', 'i', 'e', 'n', 0, '*', '.', '*', 0, 0}
	if mode == "nfo" {
		titleStr = "NFO-Datei auswählen"
		filterSlice = []uint16{'N', 'F', 'O', ' ', 'D', 'a', 't', 'e', 'i', 'e', 'n', 0, '*', '.', 'n', 'f', 'o', 0, 'A', 'l', 'l', 'e', ' ', 'D', 'a', 't', 'e', 'i', 'e', 'n', 0, '*', '.', '*', 0, 0}
	} else if mode == "cover" {
		titleStr = "Cover / Bild auswählen"
		filterSlice = []uint16{'B', 'i', 'l', 'd', 'e', 'r', 0, '*', '.', 'j', 'p', 'g', ';', '*', '.', 'j', 'p', 'e', 'g', ';', '*', '.', 'p', 'n', 'g', ';', '*', '.', 'w', 'e', 'b', 'p', 0, 'A', 'l', 'l', 'e', ' ', 'D', 'a', 't', 'e', 'i', 'e', 'n', 0, '*', '.', '*', 0, 0}
	}
	titlePtr, _ := syscall.UTF16PtrFromString(titleStr)

	var ofn openFileNameW
	ofn.lStructSize = uint32(unsafe.Sizeof(ofn))
	ofn.hwndOwner = owner
	ofn.lpstrFilter = &filterSlice[0]
	ofn.lpstrFile = &fileBuf[0]
	ofn.nMaxFile = uint32(len(fileBuf))
	ofn.lpstrTitle = titlePtr
	ofn.flags = 0x00080000 | 0x00001000 | 0x00000800 | 0x00000008

	ret, _, _ := procGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if ret == 0 {
		return "", nil
	}
	return DisplayPath(syscall.UTF16ToString(fileBuf)), nil
}
