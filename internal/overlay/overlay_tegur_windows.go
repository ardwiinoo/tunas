//go:build windows

package overlay

import "time"

// Nudge asks the owner about a long stretch on a distracting app or site.
// Safe from any goroutine; the UI thread shows it when nothing else is going on.
func (o *Overlay) Nudge(text string) {
	select {
	case o.tegurCh <- text:
	default:
	}
}

func (o *Overlay) drainTegur(now time.Time) {
	for {
		select {
		case text := <-o.tegurCh:
			if o.charHidden || o.CharacterOff || o.cal != nil {
				o.log.Debug("distraction nudge not shown (character hidden or calendar open)")
				continue
			}
			o.p.TegurDue(text, now)
		default:
			return
		}
	}
}

// copyExtensionToken puts the extension's token on the clipboard (tray item).
func (o *Overlay) copyExtensionToken(now time.Time) {
	msg := "Token ekstensi disalin. Tempel di opsi ekstensi browser."
	if o.tray.Host == nil || writeClipboard(o.tray.Host.ExtensionToken()) != nil {
		msg = "Gagal menyalin token."
	}
	o.p.Note(msg, now)
	o.sync(now)
}
