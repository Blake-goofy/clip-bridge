//go:build windows

package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/lxn/win"
)

const (
	trayCallbackMsg = win.WM_USER + 42
	trayID          = 1
	menuOpen        = 1001
	menuSend        = 1002
	menuCopy        = 1003
	menuQuit        = 1004
)

var trayApp *desktopApp

func runTray(ctx context.Context, app *desktopApp) {
	log.Printf("tray starting")
	trayApp = app

	className := syscall.StringToUTF16Ptr("ClipBridgeTrayWindow")
	hInstance := win.GetModuleHandle(nil)
	wndClass := win.WNDCLASSEX{
		CbSize:        uint32(unsafe.Sizeof(win.WNDCLASSEX{})),
		HInstance:     hInstance,
		LpszClassName: className,
		LpfnWndProc:   syscall.NewCallback(trayWndProc),
	}
	if atom := win.RegisterClassEx(&wndClass); atom == 0 {
		log.Printf("tray register class failed")
		app.setError("Tray failed", errWindows("RegisterClassEx"))
		app.openUI()
		<-ctx.Done()
		return
	}

	hwnd := win.CreateWindowEx(0, className, syscall.StringToUTF16Ptr("ClipBridge"), 0, 0, 0, 0, 0, 0, 0, hInstance, nil)
	if hwnd == 0 {
		log.Printf("tray create window failed")
		app.setError("Tray failed", errWindows("CreateWindowEx"))
		app.openUI()
		<-ctx.Done()
		return
	}

	icon := loadTrayHIcon()
	nid := notifyIconData(hwnd, icon)
	if !win.Shell_NotifyIcon(win.NIM_ADD, &nid) {
		log.Printf("tray add failed")
		app.setError("Tray failed", errWindows("Shell_NotifyIcon add"))
		app.openUI()
		<-ctx.Done()
		return
	}
	nid.UVersion = win.NOTIFYICON_VERSION
	_ = win.Shell_NotifyIcon(win.NIM_SETVERSION, &nid)
	log.Printf("tray visible")

	go func() {
		<-ctx.Done()
		win.PostMessage(hwnd, win.WM_CLOSE, 0, 0)
	}()

	var msg win.MSG
	for win.GetMessage(&msg, 0, 0, 0) > 0 {
		win.TranslateMessage(&msg)
		win.DispatchMessage(&msg)
	}

	_ = win.Shell_NotifyIcon(win.NIM_DELETE, &nid)
	if icon != 0 {
		win.DestroyIcon(icon)
	}
}

func trayWndProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case trayCallbackMsg:
		switch uint32(lParam) {
		case win.WM_LBUTTONUP:
			trayApp.openUI()
			return 0
		case win.WM_RBUTTONUP:
			showTrayMenu(hwnd)
			return 0
		}
	case win.WM_COMMAND:
		switch uint32(wParam & 0xffff) {
		case menuOpen:
			trayApp.openUI()
		case menuSend:
			go func() {
				if err := trayApp.sendClipboard(context.Background()); err != nil {
					log.Printf("send clipboard: %v", err)
				}
			}()
		case menuCopy:
			link := trayApp.snapshot()["joinLink"].(string)
			if link == "" {
				trayApp.notice("Pairing link is not ready")
				break
			}
			if err := writeClipboardTextFunc(link); err != nil {
				trayApp.notice("Copy link failed")
			} else {
				trayApp.notice("Link copied")
			}
		case menuQuit:
			trayApp.cancel()
			win.DestroyWindow(hwnd)
		}
		return 0
	case win.WM_CLOSE:
		win.DestroyWindow(hwnd)
		return 0
	case win.WM_DESTROY:
		win.PostQuitMessage(0)
		return 0
	}
	return win.DefWindowProc(hwnd, msg, wParam, lParam)
}

func notifyIconData(hwnd win.HWND, icon win.HICON) win.NOTIFYICONDATA {
	nid := win.NOTIFYICONDATA{
		CbSize:           uint32(unsafe.Sizeof(win.NOTIFYICONDATA{})),
		HWnd:             hwnd,
		UID:              trayID,
		UFlags:           win.NIF_MESSAGE | win.NIF_ICON | win.NIF_TIP,
		UCallbackMessage: trayCallbackMsg,
		HIcon:            icon,
	}
	copy(nid.SzTip[:], syscall.StringToUTF16("ClipBridge"))
	return nid
}

func showTrayMenu(hwnd win.HWND) {
	menu := win.CreatePopupMenu()
	if menu == 0 {
		return
	}
	defer win.DestroyMenu(menu)
	addMenuItem(menu, menuOpen, "Open ClipBridge")
	addMenuItem(menu, menuSend, "Send clipboard")
	addMenuItem(menu, menuCopy, "Copy pairing link")
	addMenuSeparator(menu)
	addMenuItem(menu, menuQuit, "Quit")

	var pt win.POINT
	if !win.GetCursorPos(&pt) {
		return
	}
	win.SetForegroundWindow(hwnd)
	cmd := win.TrackPopupMenu(menu, win.TPM_RETURNCMD|win.TPM_RIGHTBUTTON|win.TPM_NOANIMATION, pt.X, pt.Y, 0, hwnd, nil)
	if cmd != 0 {
		win.SendMessage(hwnd, win.WM_COMMAND, uintptr(cmd), 0)
	}
}

func addMenuItem(menu win.HMENU, id uint32, text string) {
	label := syscall.StringToUTF16(text)
	item := win.MENUITEMINFO{
		CbSize:     uint32(unsafe.Sizeof(win.MENUITEMINFO{})),
		FMask:      win.MIIM_ID | win.MIIM_STRING,
		WID:        id,
		DwTypeData: &label[0],
		Cch:        uint32(len(label) - 1),
	}
	win.InsertMenuItem(menu, ^uint32(0), true, &item)
}

func addMenuSeparator(menu win.HMENU) {
	item := win.MENUITEMINFO{
		CbSize: uint32(unsafe.Sizeof(win.MENUITEMINFO{})),
		FMask:  win.MIIM_FTYPE,
		FType:  win.MFT_SEPARATOR,
	}
	win.InsertMenuItem(menu, ^uint32(0), true, &item)
}

func loadTrayHIcon() win.HICON {
	for _, path := range trayIconPaths() {
		if path == "" {
			continue
		}
		p := syscall.StringToUTF16Ptr(path)
		h := win.LoadImage(0, p, win.IMAGE_ICON, 16, 16, win.LR_LOADFROMFILE)
		if h != 0 {
			log.Printf("tray icon loaded: %s", path)
			return win.HICON(h)
		}
	}
	log.Printf("tray icon fallback")
	return win.HICON(win.LoadIcon(0, (*uint16)(unsafe.Pointer(uintptr(win.IDI_APPLICATION)))))
}

func trayIconPaths() []string {
	exe, err := os.Executable()
	if err != nil {
		return []string{"clipbridge.ico"}
	}
	return []string{
		filepath.Join(filepath.Dir(exe), "clipbridge.ico"),
		"clipbridge.ico",
		exe,
	}
}

type windowsError string

func (e windowsError) Error() string { return string(e) }

func errWindows(name string) error { return windowsError(name + " failed") }
