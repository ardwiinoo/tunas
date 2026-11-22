//go:build windows

package overlay

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sort"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/image/font"
	"golang.org/x/sys/windows"

	"github.com/ardwiinoo/tunas/internal/agent"
	"github.com/ardwiinoo/tunas/internal/config"
	"github.com/ardwiinoo/tunas/internal/forms"
)

// The overlay is a Win32 layered window (WS_EX_LAYERED + UpdateLayeredWindow)
// drawn on the CPU. Fully transparent pixels are click-through by design, so
// only Tunas and the bubble receive the mouse. Ebitengine was dropped because
// transparent windows force OpenGL on Windows (see DECISIONS).

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	shcore   = windows.NewLazySystemDLL("shcore.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pRegisterClassExW    = user32.NewProc("RegisterClassExW")
	pCreateWindowExW     = user32.NewProc("CreateWindowExW")
	pDefWindowProcW      = user32.NewProc("DefWindowProcW")
	pDestroyWindow       = user32.NewProc("DestroyWindow")
	pShowWindow          = user32.NewProc("ShowWindow")
	pSetWindowPos        = user32.NewProc("SetWindowPos")
	pUpdateLayeredWindow = user32.NewProc("UpdateLayeredWindow")
	pGetDC               = user32.NewProc("GetDC")
	pReleaseDC           = user32.NewProc("ReleaseDC")
	pSetTimer            = user32.NewProc("SetTimer")
	pGetMessageW         = user32.NewProc("GetMessageW")
	pTranslateMessage    = user32.NewProc("TranslateMessage")
	pDispatchMessageW    = user32.NewProc("DispatchMessageW")
	pPostMessageW        = user32.NewProc("PostMessageW")
	pPostQuitMessage     = user32.NewProc("PostQuitMessage")
	pRegisterHotKey      = user32.NewProc("RegisterHotKey")
	pUnregisterHotKey    = user32.NewProc("UnregisterHotKey")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	pEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	pGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
	pSetDpiAwareness     = user32.NewProc("SetProcessDpiAwarenessContext")
	pCreateCompatibleDC  = gdi32.NewProc("CreateCompatibleDC")
	pCreateDIBSection    = gdi32.NewProc("CreateDIBSection")
	pSelectObject        = gdi32.NewProc("SelectObject")
	pDeleteObject        = gdi32.NewProc("DeleteObject")
	pDeleteDC            = gdi32.NewProc("DeleteDC")
	pGetDpiForMonitor    = shcore.NewProc("GetDpiForMonitor")
	pGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")
)

const (
	wsPopup         = 0x80000000
	wsExLayered     = 0x00080000
	wsExTopmost     = 0x00000008
	wsExToolWindow  = 0x00000080
	swHide          = 0
	swShowNoActive  = 4
	swShow          = 5
	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmMouseActivate = 0x0021
	wmChar          = 0x0102
	wmTimer         = 0x0113
	wmHotkey        = 0x0312
	wmLButtonDown   = 0x0201
	maNoActivate    = 3
	ulwAlpha        = 2
	acSrcAlpha      = 1
	modAlt          = 0x0001
	modNoRepeat     = 0x4000
	vkSpace         = 0x20
	hotkeyID        = 1
	timerID         = 1
	swpNoActivate   = 0x0010
	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	monitorPrimary  = 1
)

var hwndTopmost = ^uintptr(0) // (HWND)-1

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type point32 struct{ X, Y int32 }
type size32 struct{ CX, CY int32 }
type rect32 struct{ Left, Top, Right, Bottom int32 }

type monitorInfo struct {
	Size    uint32
	Monitor rect32
	Work    rect32
	Flags   uint32
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point32
	_       uint32
}

// ChatFunc sends typed text to the server and returns Tunas's reply.
type ChatFunc func(ctx context.Context, text string, f forms.Form) (string, error)

// Overlay is the on-screen character window.
type Overlay struct {
	cfg      config.AgentOverlay
	log      *slog.Logger
	chat     ChatFunc
	renderer Renderer

	formCh  chan forms.Output
	replyCh chan chatResult

	// UI-thread state
	ctx       context.Context
	hwnd      uintptr
	p         Presenter
	shown     bool
	scale     float64
	canvas    *Canvas
	face      font.Face
	measure   func(string) float64
	scene     Scene
	sceneText string
	formStart time.Time
	motionAt  time.Time
	pos       point32
	memDC     uintptr
	dib       uintptr
	bits      []byte
	surrogate uint16
}

var theOverlay *Overlay // the window procedure needs a global

// New creates the overlay. Run must be called on the main goroutine.
func New(cfg config.AgentOverlay, chat ChatFunc, log *slog.Logger) (*Overlay, error) {
	mf, err := NewFace(fontSize)
	if err != nil {
		return nil, err
	}
	return &Overlay{
		cfg: cfg, log: log, chat: chat, renderer: Procedural{},
		formCh: make(chan forms.Output, 8), replyCh: make(chan chatResult, 1),
		measure: Measurer(mf), scale: 1,
	}, nil
}

// chatResult is the outcome of one chat call.
type chatResult struct {
	reply string
	fail  agent.ChatFail // "" on success
}

const (
	chatTimeout  = 25 * time.Second // the server gives up on the LLM after ~20 s
	fontSize     = 14
	visibleTick  = 66  // ms (~15 fps; slow animations do not need more)
	hiddenTick   = 200 // ms (5 ticks/s, nothing drawn)
	defaultClass = "TunasOverlay"
)

// Notify passes a new form output to the overlay (safe from any goroutine).
func (o *Overlay) Notify(out forms.Output) {
	select {
	case o.formCh <- out:
	default:
	}
}

// Run creates the window and pumps messages until ctx is cancelled.
func (o *Overlay) Run(ctx context.Context) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	o.ctx = ctx
	theOverlay = o

	pSetDpiAwareness.Call(^uintptr(3)) //nolint:errcheck // PER_MONITOR_AWARE_V2 (-4)

	inst, _, _ := pGetModuleHandleW.Call(0)
	cls, _ := windows.UTF16PtrFromString(defaultClass)
	title, _ := windows.UTF16PtrFromString("Tunas")
	wc := wndClassEx{WndProc: windows.NewCallback(wndProc), Instance: inst, ClassName: cls}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return errors.New("RegisterClassEx: " + err.Error())
	}
	hwnd, _, err := pCreateWindowExW.Call(wsExLayered|wsExTopmost|wsExToolWindow,
		uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)), wsPopup,
		0, 0, WindowW, WindowH, 0, 0, inst, 0)
	if hwnd == 0 {
		return errors.New("CreateWindowEx: " + err.Error())
	}
	o.hwnd = hwnd
	defer o.freeDIB()

	if r, _, err := pRegisterHotKey.Call(hwnd, hotkeyID, modAlt|modNoRepeat, vkSpace); r == 0 {
		o.log.Warn("hotkey Alt+Space unavailable (used by another app?)", "err", err.Error())
	} else {
		defer pUnregisterHotKey.Call(hwnd, hotkeyID) //nolint:errcheck
	}
	pSetTimer.Call(hwnd, timerID, hiddenTick, 0) //nolint:errcheck

	go func() {
		<-ctx.Done()
		pPostMessageW.Call(hwnd, wmClose, 0, 0) //nolint:errcheck
	}()

	var m winMsg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return nil
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m))) //nolint:errcheck
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m))) //nolint:errcheck
	}
}

func wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	o := theOverlay
	if o == nil || hwnd != o.hwnd {
		r, _, _ := pDefWindowProcW.Call(hwnd, msg, wp, lp)
		return r
	}
	now := time.Now()
	switch msg {
	case wmTimer:
		o.tick(now)
		return 0
	case wmHotkey:
		o.p.OnHotkey(now)
		o.sync(now)
		if o.p.Typing() {
			pSetForegroundWindow.Call(hwnd) //nolint:errcheck
		}
		return 0
	case wmChar:
		o.onChar(uint16(wp), now)
		return 0
	case wmLButtonDown:
		o.p.Dismiss(now)
		o.sync(now)
		return 0
	case wmMouseActivate:
		if !o.p.Typing() {
			return maNoActivate // clicking Tunas must not steal focus
		}
	case wmClose:
		pDestroyWindow.Call(hwnd) //nolint:errcheck
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0) //nolint:errcheck
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func (o *Overlay) onChar(u uint16, now time.Time) {
	if !o.p.Typing() {
		return
	}
	switch {
	case u == 0x08:
		o.p.Backspace(now)
	case u == 0x0D:
		if text := o.p.Submit(now); text != "" {
			go o.ask(text, o.p.Form)
		}
	case u == 0x1B:
		o.p.CancelInput(now)
	case utf16.IsSurrogate(rune(u)) && o.surrogate == 0:
		o.surrogate = u
		return
	case o.surrogate != 0:
		o.p.Type([]rune{utf16.DecodeRune(rune(o.surrogate), rune(u))}, now)
		o.surrogate = 0
	default:
		o.p.Type([]rune{rune(u)}, now)
	}
	o.sync(now)
}

// ask runs one chat call off the UI thread. The presenter locks input while
// waiting, so at most one call is in flight.
func (o *Overlay) ask(text string, f forms.Form) {
	ctx, cancel := context.WithTimeout(o.ctx, chatTimeout)
	defer cancel()
	start := time.Now()
	reply, err := o.chat(ctx, text, f)
	res := chatResult{reply: reply}
	if err != nil {
		res.fail = agent.ClassifyChatError(err)
		// the token is only ever in a request header, never in err
		o.log.Warn("chat failed", "kind", string(res.fail), "err", err.Error(), "ms", time.Since(start).Milliseconds())
	} else {
		o.log.Info("chat ok", "ms", time.Since(start).Milliseconds())
	}
	select {
	case o.replyCh <- res:
	case <-o.ctx.Done():
	}
}

func (o *Overlay) tick(now time.Time) {
	wasTyping := o.p.Typing()
	for drained := false; !drained; {
		select {
		case out := <-o.formCh:
			if out.Form != o.p.Form {
				o.formStart = now
			}
			if out.Motion {
				o.motionAt = now
			}
			o.p.OnForm(out, now)
		case r := <-o.replyCh:
			if r.fail != "" {
				o.p.OnChatFailed(FailText(r.fail), now)
			} else {
				o.p.OnReply(r.reply, now)
			}
		default:
			drained = true
		}
	}
	o.p.Tick(now)
	if o.p.Typing() && !wasTyping { // failed: the text box is back for a retry
		pSetForegroundWindow.Call(o.hwnd) //nolint:errcheck
	}
	o.sync(now)
}

// sync applies the presenter state: show/hide, timer rate, and redraw.
func (o *Overlay) sync(now time.Time) {
	if o.p.Visible() != o.shown {
		o.shown = o.p.Visible()
		if o.shown {
			o.place()
			cmd := uintptr(swShowNoActive)
			if o.p.Typing() {
				cmd = swShow
			}
			o.render(now)                                                                          // content first, then show: no flash of stale frame
			pShowWindow.Call(o.hwnd, cmd)                                                          //nolint:errcheck
			pSetWindowPos.Call(o.hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate) //nolint:errcheck
			pSetTimer.Call(o.hwnd, timerID, visibleTick, 0)                                        //nolint:errcheck
		} else {
			pShowWindow.Call(o.hwnd, swHide)               //nolint:errcheck
			pSetTimer.Call(o.hwnd, timerID, hiddenTick, 0) //nolint:errcheck
		}
		return
	}
	if o.shown {
		o.render(now)
	}
}

// monitors returns HMONITORs with the primary first.
func monitors() []uintptr {
	var hs []uintptr
	cb := windows.NewCallback(func(h, _, _, _ uintptr) uintptr { hs = append(hs, h); return 1 })
	pEnumDisplayMonitors.Call(0, 0, cb, 0) //nolint:errcheck
	sort.SliceStable(hs, func(i, j int) bool { return isPrimary(hs[i]) && !isPrimary(hs[j]) })
	return hs
}

func monitorInfoOf(h uintptr) monitorInfo {
	mi := monitorInfo{}
	mi.Size = uint32(unsafe.Sizeof(mi))
	pGetMonitorInfoW.Call(h, uintptr(unsafe.Pointer(&mi))) //nolint:errcheck
	return mi
}

func isPrimary(h uintptr) bool { return monitorInfoOf(h).Flags&monitorPrimary != 0 }

// place picks the configured monitor, its DPI scale and the corner position.
func (o *Overlay) place() {
	ms := monitors()
	if len(ms) == 0 {
		return
	}
	h := ms[0]
	if o.cfg.Monitor > 0 && o.cfg.Monitor < len(ms) {
		h = ms[o.cfg.Monitor]
	}
	var dx, dy uint32 = 96, 96
	pGetDpiForMonitor.Call(h, 0, uintptr(unsafe.Pointer(&dx)), uintptr(unsafe.Pointer(&dy))) //nolint:errcheck
	scale := float64(dx) / 96
	if scale <= 0 {
		scale = 1
	}
	if scale != o.scale || o.canvas == nil {
		o.scale = scale
		o.canvas = NewCanvas(int(WindowW*scale), int(WindowH*scale))
		if f, err := NewFace(fontSize * scale); err == nil {
			o.face = f
		}
		o.freeDIB()
		o.sceneText = "\x00" // force relayout
	}
	w := monitorInfoOf(h).Work
	lw := int(float64(w.Right-w.Left) / scale)
	lh := int(float64(w.Bottom-w.Top) / scale)
	x, y := WindowPosition(o.cfg.Corner, lw, lh, o.cfg.MarginX, o.cfg.MarginY)
	o.pos = point32{w.Left + int32(float64(x)*scale), w.Top + int32(float64(y)*scale)}
}

func (o *Overlay) render(now time.Time) {
	if o.canvas == nil || o.face == nil {
		o.place()
		if o.canvas == nil || o.face == nil {
			return
		}
	}
	txt := o.p.BubbleText()
	if txt != o.sceneText {
		o.scene = ComputeScene(o.cfg.Corner, txt, o.measure)
		o.sceneText = txt
	}
	motion := 0.0
	if d := now.Sub(o.motionAt).Seconds(); d < 1 {
		motion = 1 - d
	}
	sc := o.scene
	sc.Lines = WithDots(sc.Lines, o.p.Dots(now))
	a := Anim{T: now.Sub(o.formStart).Seconds(), Motion: motion,
		Thinking: o.p.Waiting(), Think: o.p.WaitingFor(now).Seconds()}
	Frame(o.canvas, o.renderer, sc, o.p.Form, o.face, a, o.scale)
	o.present()
}

func (o *Overlay) ensureDIB(w, h int) bool {
	if o.memDC != 0 && len(o.bits) == w*h*4 {
		return true
	}
	o.freeDIB()
	screen, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, screen) //nolint:errcheck
	o.memDC, _, _ = pCreateCompatibleDC.Call(screen)
	bi := bitmapInfoHeader{Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	var bits unsafe.Pointer
	o.dib, _, _ = pCreateDIBSection.Call(o.memDC, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if o.dib == 0 || bits == nil {
		o.log.Error("CreateDIBSection failed")
		o.freeDIB()
		return false
	}
	pSelectObject.Call(o.memDC, o.dib) //nolint:errcheck
	o.bits = unsafe.Slice((*byte)(bits), w*h*4)
	return true
}

func (o *Overlay) freeDIB() {
	if o.dib != 0 {
		pDeleteObject.Call(o.dib) //nolint:errcheck
		o.dib = 0
	}
	if o.memDC != 0 {
		pDeleteDC.Call(o.memDC) //nolint:errcheck
		o.memDC = 0
	}
	o.bits = nil
}

// present copies the canvas (premultiplied RGBA) into the DIB (premultiplied
// BGRA) and updates the layered window.
func (o *Overlay) present() {
	img := o.canvas.Img
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if !o.ensureDIB(w, h) {
		return
	}
	src, dst := img.Pix, o.bits
	for i := 0; i+3 < len(src); i += 4 {
		dst[i], dst[i+1], dst[i+2], dst[i+3] = src[i+2], src[i+1], src[i], src[i+3]
	}
	screen, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, screen) //nolint:errcheck
	sz := size32{int32(w), int32(h)}
	var origin point32
	blend := [4]byte{0, 0, 255, acSrcAlpha} // AC_SRC_OVER, 0, alpha 255, AC_SRC_ALPHA
	pUpdateLayeredWindow.Call(o.hwnd, screen, uintptr(unsafe.Pointer(&o.pos)), uintptr(unsafe.Pointer(&sz)),
		o.memDC, uintptr(unsafe.Pointer(&origin)), 0, uintptr(unsafe.Pointer(&blend)), ulwAlpha) //nolint:errcheck
}
