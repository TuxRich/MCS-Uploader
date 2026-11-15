package main

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"
)

// Windows' own folder chooser (the file dialog, set to pick folders). walk
// only has the older tree of folders, which can't be told where to start.

var (
	clsidFileOpenDialog = win.CLSID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidFileOpenDialog   = win.IID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	iidShellItem        = win.IID{Data1: 0x43826D1E, Data2: 0xE718, Data3: 0x42EE, Data4: [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}

	procSHCreateItemFromParsingName = syscall.NewLazyDLL("shell32.dll").NewProc("SHCreateItemFromParsingName")

	// What the dialog's methods hand back. Kept here, rather than in local
	// variables, so that they stay put while Windows writes to them. Only
	// used on the UI thread.
	folderDlg struct {
		dialog  unsafe.Pointer
		options uint32
		item    unsafe.Pointer
		name    *uint16
	}
)

// Method numbers in the dialog's and a shell item's tables.
const (
	comRelease = 2

	dlgShow       = 3
	dlgSetOptions = 9
	dlgGetOptions = 10
	dlgSetFolder  = 12
	dlgSetTitle   = 17
	dlgGetResult  = 20

	itemGetDisplayName = 5

	fosPickFolders     = 0x20
	fosForceFileSystem = 0x40
	sigdnFileSysPath   = 0x80058000
)

// comCall calls a method of a COM object by its number in the object's table.
func comCall(obj unsafe.Pointer, method int, args ...uintptr) uintptr {
	table := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(table, uintptr(method)*unsafe.Sizeof(uintptr(0))))
	ret, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return ret
}

// chooseFolder shows the folder chooser, opened at start if that is a folder,
// and returns the folder picked. ok is false if it was cancelled.
func chooseFolder(owner walk.Form, title, start string) (folder string, ok bool, err error) {
	if hr := win.OleInitialize(); hr != win.S_OK && hr != win.S_FALSE {
		return "", false, fmt.Errorf("OleInitialize: %#x", uint32(hr))
	}
	defer win.OleUninitialize()

	d := &folderDlg
	d.dialog, d.item, d.name = nil, nil, nil
	if hr := win.CoCreateInstance(&clsidFileOpenDialog, nil, win.CLSCTX_INPROC_SERVER, &iidFileOpenDialog, &d.dialog); win.FAILED(hr) || d.dialog == nil {
		return "", false, fmt.Errorf("the folder chooser couldn't be opened: %#x", uint32(hr))
	}
	defer comCall(d.dialog, comRelease)

	comCall(d.dialog, dlgGetOptions, uintptr(unsafe.Pointer(&d.options)))
	comCall(d.dialog, dlgSetOptions, uintptr(d.options|fosPickFolders|fosForceFileSystem))
	if p, err := syscall.UTF16PtrFromString(title); err == nil {
		comCall(d.dialog, dlgSetTitle, uintptr(unsafe.Pointer(p)))
		runtime.KeepAlive(p)
	}
	if start != "" {
		if p, err := syscall.UTF16PtrFromString(start); err == nil {
			hr, _, _ := procSHCreateItemFromParsingName.Call(uintptr(unsafe.Pointer(p)), 0,
				uintptr(unsafe.Pointer(&iidShellItem)), uintptr(unsafe.Pointer(&d.item)))
			runtime.KeepAlive(p)
			if hr == 0 && d.item != nil {
				comCall(d.dialog, dlgSetFolder, uintptr(d.item))
				comCall(d.item, comRelease)
			}
			d.item = nil
		}
	}

	var hwnd win.HWND
	if owner != nil {
		hwnd = owner.Handle()
	}
	if hr := comCall(d.dialog, dlgShow, uintptr(hwnd)); hr != 0 {
		return "", false, nil // cancelled
	}
	if hr := comCall(d.dialog, dlgGetResult, uintptr(unsafe.Pointer(&d.item))); hr != 0 || d.item == nil {
		return "", false, fmt.Errorf("the folder chooser gave no folder: %#x", uint32(hr))
	}
	defer comCall(d.item, comRelease)
	if hr := comCall(d.item, itemGetDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&d.name))); hr != 0 || d.name == nil {
		return "", false, errors.New("that isn't a folder on a disk")
	}
	defer win.CoTaskMemFree(uintptr(unsafe.Pointer(d.name)))
	folder = win.UTF16PtrToString(d.name)
	return folder, folder != "", nil
}
