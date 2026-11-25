//go:build windows

package overlay

import (
	"context"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ardwiinoo/tunas/internal/agent"
)

// The tray icon lives on the overlay window: its messages arrive in the same
// wndProc, so no second message loop or thread is needed.

var (
	shell32 = windows.NewLazySystemDLL("shell32.dll")

	pShellNotifyIconW       = shell32.NewProc("Shell_NotifyIconW")
	pShellExecuteW          = shell32.NewProc("ShellExecuteW")
	pCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	pAppendMenuW            = user32.NewProc("AppendMenuW")
	pTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	pDestroyMenu            = user32.NewProc("DestroyMenu")
	pGetCursorPos           = user32.NewProc("GetCursorPos")
	pCreateIconIndirect     = user32.NewProc("CreateIconIndirect")
	pDestroyIcon            = user32.NewProc("DestroyIcon")
	pGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	pRegisterWindowMessageW = user32.NewProc("RegisterWindowMessageW")
	pCreateBitmap           = gdi32.NewProc("CreateBitmap")
)

const (
	nimAdd       = 0
	nimModify    = 1
	nimDelete    = 2
	nifMessage   = 0x1
	nifIcon      = 0x2
	nifTip       = 0x4
	wmApp        = 0x8000
	wmTray       = wmApp + 1
	wmNull       = 0x0000
	wmLButtonUp  = 0x0202
	wmRButtonUp  = 0x0205
	mfString     = 0x0
	mfGrayed     = 0x1
	mfChecked    = 0x8
	mfSeparator  = 0x800
	tpmReturnCmd = 0x0100
	tpmRightBtn  = 0x0002
	tpmNoNotify  = 0x0080
	smCxSmIcon   = 49
	swShowNormal = 1
	trayID       = 1
	cmdPause     = 1
	cmdLog       = 2
	cmdQuit      = 3
	stateNotSet  = -1
)

type notifyIconData struct {
	Size            uint32
	Wnd             uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUID            windows.GUID
	BalloonIcon     uintptr
}

type iconInfo struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	Mask     uintptr
	Color    uintptr
}

var taskbarCreated uintptr // registered message: Explorer restarted, re-add the icon

// trayInit adds the icon (call on the UI thread after the window exists).
func (o *Overlay) trayInit() {
	name, _ := windows.UTF16PtrFromString("TaskbarCreated")
	taskbarCreated, _, _ = pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(name)))
	o.trayShown = stateNotSet
	o.trayUpdate(nimAdd)
}

// trayUpdate redraws the icon and tooltip for the current state.
func (o *Overlay) trayUpdate(op uintptr) {
	st := agent.ConnState(o.conn.Load())
	size := int(o.scale * 16)
	if s, _, _ := pGetSystemMetrics.Call(smCxSmIcon); s > 0 {
		size = int(s)
	}
	col := TrayColor(st)
	if o.paused {
		col = ColPaused
	}
	icon := makeIcon(TrayIconColor(col, size), size)
	if icon == 0 {
		o.log.Warn("tray icon could not be created")
		return
	}
	nid := notifyIconData{Wnd: o.hwnd, ID: trayID, Flags: nifMessage | nifIcon | nifTip,
		CallbackMessage: wmTray, Icon: icon}
	nid.Size = uint32(unsafe.Sizeof(nid))
	tip, _ := windows.UTF16FromString(TrayTip(st, o.paused))
	copy(nid.Tip[:len(nid.Tip)-1], tip)
	if r, _, _ := pShellNotifyIconW.Call(op, uintptr(unsafe.Pointer(&nid))); r == 0 && op == nimModify {
		// Explorer may have lost the icon: add it again
		pShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid))) //nolint:errcheck
	}
	if o.trayIcon != 0 {
		pDestroyIcon.Call(o.trayIcon) //nolint:errcheck
	}
	o.trayIcon = icon
	o.trayShown = int(st)
	o.trayPaused = o.paused
}

// trayTick refreshes the icon when the connection state changed.
func (o *Overlay) trayTick() {
	if int(o.conn.Load()) != o.trayShown || o.paused != o.trayPaused {
		o.trayUpdate(nimModify)
	}
}

func (o *Overlay) trayRemove() {
	nid := notifyIconData{Wnd: o.hwnd, ID: trayID}
	nid.Size = uint32(unsafe.Sizeof(nid))
	pShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid))) //nolint:errcheck
	if o.trayIcon != 0 {
		pDestroyIcon.Call(o.trayIcon) //nolint:errcheck
		o.trayIcon = 0
	}
}

// trayMenu shows the right-click menu and runs the chosen command.
func (o *Overlay) trayMenu() {
	m, _, _ := pCreatePopupMenu.Call()
	if m == 0 {
		return
	}
	defer pDestroyMenu.Call(m) //nolint:errcheck
	info := MenuInfo{
		Status: "Tunas", Queued: int(o.wlQueue.Load()), Paused: o.paused,
		WorklogBubbles: o.tray.State.WorklogBubbles, Projects: o.tray.State.ProjectsEnabled, Domains: o.tray.State.DomainsEnabled, CharacterShown: !o.charHidden,
		HasLog: o.LogPath != "", HasConfigDir: o.tray.ConfigDir != "",
	}
	if o.tray.Host != nil {
		info.Status = o.tray.Host.Status()
	}
	if o.tray.Registry != nil && o.tray.ExePath != "" {
		// the registry is the source of truth: read it every time the menu opens
		on, stale, err := agent.AutostartStatus(o.tray.Registry, o.tray.ExePath)
		info.AutostartAvail, info.AutostartOn, info.AutostartStale = err == nil, on, stale
	}
	for _, it := range TrayMenu(info) {
		flags := uintptr(mfString)
		switch {
		case it.Sep:
			flags = mfSeparator
		case it.Grayed:
			flags |= mfGrayed
		}
		if it.Checked {
			flags |= mfChecked
		}
		t, _ := windows.UTF16PtrFromString(it.Text)
		pAppendMenuW.Call(m, flags, uintptr(it.ID), uintptr(unsafe.Pointer(t))) //nolint:errcheck
	}

	var pt point32
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))) //nolint:errcheck
	// required so the menu closes when the user clicks elsewhere
	pSetForegroundWindow.Call(o.hwnd) //nolint:errcheck
	cmd, _, _ := pTrackPopupMenu.Call(m, tpmReturnCmd|tpmRightBtn|tpmNoNotify,
		uintptr(pt.X), uintptr(pt.Y), 0, o.hwnd, 0)
	pPostMessageW.Call(o.hwnd, wmNull, 0, 0) //nolint:errcheck
	o.trayCommand(int(cmd), info)
}

func (o *Overlay) trayCommand(cmd int, info MenuInfo) {
	now := time.Now()
	switch cmd {
	case CmdAutostart, CmdAutostartFix:
		on := cmd == CmdAutostartFix || !info.AutostartOn
		if err := agent.SetAutostart(o.tray.Registry, o.tray.ExePath, on); err != nil {
			o.log.Warn("autostart not changed", "err", err)
		} else {
			o.log.Info("autostart", "on", on)
		}
	case CmdPause:
		o.paused = !o.paused
		if o.OnPause != nil {
			o.OnPause(o.paused)
		}
		o.saveState()
		o.trayUpdate(nimModify)
	case CmdDomains:
		o.tray.State.DomainsEnabled = !o.tray.State.DomainsEnabled
		if o.OnDomains != nil {
			o.OnDomains(o.tray.State.DomainsEnabled)
		}
		o.saveState()
	case CmdCopyToken:
		o.copyExtensionToken(now)
	case CmdProjects:
		o.tray.State.ProjectsEnabled = !o.tray.State.ProjectsEnabled
		if o.OnProjects != nil {
			o.OnProjects(o.tray.State.ProjectsEnabled)
		}
		o.saveState()
	case CmdWorklogBubbles:
		o.tray.State.WorklogBubbles = !o.tray.State.WorklogBubbles
		o.saveState()
	case CmdMuteWorklog:
		if o.tray.Host != nil {
			go func() {
				ctx, cancel := context.WithTimeout(o.ctx, 15*time.Second)
				defer cancel()
				if err := o.tray.Host.MuteWorklog(ctx); err != nil {
					o.log.Info("worklog not muted (server unreachable)", "err", err)
				}
			}()
		}
		o.p.Note(TextWorklogMuted, now)
		o.sync(now)
	case CmdCalendar:
		o.openCalendar(now)
	case CmdStatus:
		o.askStatus(now)
		pSetForegroundWindow.Call(o.hwnd) //nolint:errcheck
	case CmdClipboard:
		o.clipboardWorklog(now)
		pSetForegroundWindow.Call(o.hwnd) //nolint:errcheck
	case CmdWriteWorklog:
		o.p.OpenWorklog(0, now)
		o.sync(now)
		pSetForegroundWindow.Call(o.hwnd) //nolint:errcheck
	case CmdCharacter:
		o.charHidden = !o.charHidden
		o.saveState()
	case CmdLog:
		o.shellOpen(o.LogPath)
	case CmdConfigDir:
		o.shellOpen(o.tray.ConfigDir)
	case CmdQuit:
		o.log.Info("quit from tray menu")
		if o.OnQuit != nil {
			o.OnQuit()
		}
	}
}

func (o *Overlay) shellOpen(path string) {
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(path)
	if r, _, _ := pShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), 0, 0, swShowNormal); r <= 32 {
		o.log.Warn("open failed", "code", r)
	}
}

// makeIcon builds an HICON from straight-alpha BGRA pixels.
func makeIcon(bgra []byte, size int) uintptr {
	screen, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, screen) //nolint:errcheck
	bi := bitmapInfoHeader{Width: int32(size), Height: -int32(size), Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	var bits unsafe.Pointer
	color, _, _ := pCreateDIBSection.Call(screen, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if color == 0 || bits == nil {
		return 0
	}
	defer pDeleteObject.Call(color) //nolint:errcheck
	copy(unsafe.Slice((*byte)(bits), size*size*4), bgra)
	mask, _, _ := pCreateBitmap.Call(uintptr(size), uintptr(size), 1, 1, 0)
	if mask == 0 {
		return 0
	}
	defer pDeleteObject.Call(mask) //nolint:errcheck
	ii := iconInfo{FIcon: 1, Mask: mask, Color: color}
	h, _, _ := pCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	return h
}
