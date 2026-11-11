//go:build windows

package main

import (
	"errors"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// openFileName mirrors OPENFILENAMEW from commdlg.h; the field order and
// the pointers' alignment are what the dialog expects.
type openFileName struct {
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

var (
	comdlg32            = syscall.NewLazyDLL("comdlg32.dll")
	procGetOpenFileName = comdlg32.NewProc("GetOpenFileNameW")
)

// PickFile asks Windows for a file, the way any other program does.
func PickFile() (string, error) {
	buf := make([]uint16, 1024)
	filter := utf16(`所有文件` + "\x00" + `*.*` + "\x00\x00")
	title := utf16("选择要发送的文件")

	ofn := openFileName{
		lpstrFilter: filter,
		lpstrFile:   &buf[0],
		nMaxFile:    uint32(len(buf)),
		lpstrTitle:  title,
		flags: 0x00080000 | // OFN_EXPLORER
			0x00001000 | // OFN_FILEMUSTEXIST
			0x00000800 | // OFN_PATHMUSTEXIST
			0x00000008, // OFN_NOCHANGEDIR
	}
	ofn.lStructSize = uint32(unsafe.Sizeof(ofn))

	ok, _, _ := procGetOpenFileName.Call(uintptr(unsafe.Pointer(&ofn)))
	if ok == 0 {
		return "", errors.New("没有选择文件")
	}
	return syscall.UTF16ToString(buf), nil
}

func utf16(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}

// The folder picker lives in shell32; the item id list it returns must be
// released with CoTaskMemFree.
var (
	shell32             = syscall.NewLazyDLL("shell32.dll")
	procBrowseForFolder = shell32.NewProc("SHBrowseForFolderW")
	procPathFromIDList  = shell32.NewProc("SHGetPathFromIDListW")
	ole32               = syscall.NewLazyDLL("ole32.dll")
	procCoTaskMemFree   = ole32.NewProc("CoTaskMemFree")
)

// browseInfo mirrors BROWSEINFOW.
type browseInfo struct {
	hwndOwner      uintptr
	pidlRoot       uintptr
	pszDisplayName *uint16
	lpszTitle      *uint16
	ulFlags        uint32
	lpfn           uintptr
	lParam         uintptr
	iImage         int32
}

// PickFolder asks Windows for a directory, for where received files go.
func PickFolder(title string) (string, error) {
	display := make([]uint16, 260)
	bi := browseInfo{
		pszDisplayName: &display[0],
		lpszTitle:      utf16(title),
		// BIF_RETURNONLYFSDIRS | BIF_NEWDIALOGSTYLE | BIF_EDITBOX
		ulFlags: 0x00000001 | 0x00000040 | 0x00000010,
	}
	pidl, _, _ := procBrowseForFolder.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return "", errors.New("没有选择文件夹")
	}
	defer procCoTaskMemFree.Call(pidl)

	buf := make([]uint16, 260)
	ok, _, _ := procPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&buf[0])))
	if ok == 0 {
		return "", errors.New("这个文件夹读不出路径")
	}
	return syscall.UTF16ToString(buf), nil
}

// PickFiles asks Windows for one or more files. The dialog returns the
// folder first, then each name, separated by NUL; a single file comes back
// as a full path instead.
func PickFiles() ([]string, error) {
	buf := make([]uint16, 64*1024)
	filter := utf16(`所有文件` + "\x00" + `*.*` + "\x00\x00")
	title := utf16("选择要发送的文件（可多选）")

	ofn := openFileName{
		lpstrFilter: filter,
		lpstrFile:   &buf[0],
		nMaxFile:    uint32(len(buf)),
		lpstrTitle:  title,
		flags: 0x00080000 | // OFN_EXPLORER
			0x00001000 | // OFN_FILEMUSTEXIST
			0x00000800 | // OFN_PATHMUSTEXIST
			0x00000200 | // OFN_ALLOWMULTISELECT
			0x00000008, // OFN_NOCHANGEDIR
	}
	ofn.lStructSize = uint32(unsafe.Sizeof(ofn))

	ok, _, _ := procGetOpenFileName.Call(uintptr(unsafe.Pointer(&ofn)))
	if ok == 0 {
		return nil, errors.New("没有选择文件")
	}

	// Walk the NUL-separated list the dialog filled in.
	var parts []string
	start := 0
	for i := 0; i < len(buf); i++ {
		if buf[i] != 0 {
			continue
		}
		if i == start {
			break
		}
		parts = append(parts, syscall.UTF16ToString(buf[start:i]))
		start = i + 1
	}
	if len(parts) == 0 {
		return nil, errors.New("没有选择文件")
	}
	if len(parts) == 1 {
		return parts, nil
	}
	dir := parts[0]
	out := make([]string, 0, len(parts)-1)
	for _, name := range parts[1:] {
		out = append(out, dir+`\`+name)
	}
	return out, nil
}

// allowForeground lets the program started next take the foreground, which
// a process that does not own it may not do by itself: without this the
// folder Windows opens lands behind the window that asked for it.
func allowForeground() {
	user32.NewProc("AllowSetForegroundWindow").Call(0xFFFFFFFF) // ASFW_ANY
}

// The window Windows opens for a folder is not ours, so bringing it to the
// front takes more than asking: SetForegroundWindow is refused unless the
// caller already owns the foreground, while SwitchToThisWindow is not.
var (
	procGetWindowText  = user32.NewProc("GetWindowTextW")
	procGetClassName   = user32.NewProc("GetClassNameW")
	procEnumWindows    = user32.NewProc("EnumWindows")
	procSetForeground  = user32.NewProc("SetForegroundWindow")
	procBringWindowTop = user32.NewProc("BringWindowToTop")
	procShowWindow     = user32.NewProc("ShowWindow")
	procSwitchToThis   = user32.NewProc("SwitchToThisWindow")
)

func windowText(proc *syscall.LazyProc, h uintptr) string {
	buf := make([]uint16, 512)
	_, _, _ = proc.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

// findFolderWindow looks for the Explorer window showing a folder.
func findFolderWindow(folder string) uintptr {
	var found uintptr
	cb := syscall.NewCallback(func(h uintptr, _ uintptr) uintptr {
		if windowText(procGetClassName, h) != "CabinetWClass" {
			return 1
		}
		if strings.Contains(windowText(procGetWindowText, h), folder) {
			found = h
			return 0
		}
		return 1
	})
	_, _, _ = procEnumWindows.Call(cb, 0)
	return found
}

// bringFolderToFront waits for the folder window to appear and lifts it,
// because Windows opens it behind the window that asked for it.
func bringFolderToFront(folder string) {
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(120 * time.Millisecond)
			h := findFolderWindow(folder)
			if h == 0 {
				continue
			}
			_, _, _ = procShowWindow.Call(h, 9) // SW_RESTORE
			_, _, _ = procBringWindowTop.Call(h)
			_, _, _ = procSetForeground.Call(h)
			_, _, _ = procSwitchToThis.Call(h, 1)
			return
		}
	}()
}
