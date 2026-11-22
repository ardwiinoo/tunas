package bot

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	tg "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// pollTimeout is the getUpdates long-poll timeout (the library sends pollTimeout-1s).
const pollTimeout = 51 * time.Second

// Telegram connects Commands to the Telegram Bot API via long polling.
type Telegram struct {
	token   string
	ownerID int64
	log     *slog.Logger
	cmds    *Commands
	bot     *tg.Bot
	// OnCallback handles inline-button callbacks (Phase 3). Optional.
	OnCallback func(ctx context.Context, b *tg.Bot, q *models.CallbackQuery)
	// OnText handles non-command text (Phase 3). Optional; returns reply.
	OnText func(ctx context.Context, text string) string
}

// NewTelegram creates the bot client. It does not contact Telegram yet.
func NewTelegram(token string, ownerID int64, cmds *Commands, log *slog.Logger) (*Telegram, error) {
	t := &Telegram{token: token, ownerID: ownerID, log: log, cmds: cmds}
	b, err := tg.New(token,
		tg.WithSkipGetMe(),
		tg.WithHTTPClient(pollTimeout, &http.Client{Timeout: pollTimeout + 10*time.Second}),
		tg.WithAllowedUpdates(tg.AllowedUpdates{"message", "callback_query"}),
		tg.WithDefaultHandler(t.handle),
		tg.WithErrorsHandler(func(err error) { log.Warn("telegram error", "err", t.Redact(err)) }),
		tg.WithDebugHandler(func(string, ...any) {}),
	)
	if err != nil {
		return nil, errors.New("telegram client: " + t.Redact(err))
	}
	t.bot = b
	return t, nil
}

// Redact removes the bot token from error text (request URLs contain it).
func (t *Telegram) Redact(err error) string {
	if err == nil {
		return ""
	}
	return RedactToken(err.Error(), t.token)
}

// RedactToken replaces every occurrence of token in s.
func RedactToken(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "<redacted>")
}

// Run deletes any webhook and long-polls until ctx is cancelled.
func (t *Telegram) Run(ctx context.Context) error {
	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := t.bot.DeleteWebhook(initCtx, &tg.DeleteWebhookParams{}); err != nil {
		t.log.Warn("deleteWebhook failed", "err", t.Redact(err))
	}
	me, err := t.bot.GetMe(initCtx)
	if err != nil {
		t.log.Warn("getMe failed", "err", t.Redact(err))
	} else {
		t.log.Info("telegram bot connected", "username", me.Username)
	}
	t.bot.Start(ctx) // blocks until ctx done
	return nil
}

// IsOwner reports whether an update comes from the owner.
func IsOwner(u *models.Update, ownerID int64) bool {
	switch {
	case u.Message != nil:
		return u.Message.From != nil && u.Message.From.ID == ownerID && u.Message.Chat.ID == ownerID
	case u.CallbackQuery != nil:
		return u.CallbackQuery.From.ID == ownerID
	}
	return false
}

func (t *Telegram) handle(ctx context.Context, b *tg.Bot, u *models.Update) {
	if !IsOwner(u, t.ownerID) {
		t.log.Debug("ignored update from non-owner", "update_id", u.ID)
		return
	}
	if u.CallbackQuery != nil {
		if t.OnCallback != nil {
			t.OnCallback(ctx, b, u.CallbackQuery)
		}
		return
	}
	text := u.Message.Text
	if text == "" {
		return
	}
	var reply string
	if _, _, isCmd := SplitCommand(text); !isCmd && t.OnText != nil {
		reply = t.OnText(ctx, text)
	} else {
		reply = t.cmds.Handle(ctx, text)
	}
	if reply == "" {
		return
	}
	if err := t.Send(ctx, u.Message.Chat.ID, reply); err != nil {
		t.log.Warn("reply failed", "err", err)
	}
}

// Send sends a plain-text message.
func (t *Telegram) Send(ctx context.Context, chatID int64, text string) error {
	_, err := t.bot.SendMessage(ctx, &tg.SendMessageParams{ChatID: chatID, Text: text})
	if err != nil {
		return errors.New(t.Redact(err))
	}
	return nil
}

// NotifyOwner sends a message to the owner's private chat.
func (t *Telegram) NotifyOwner(ctx context.Context, text string) error {
	return t.Send(ctx, t.ownerID, text)
}

// Bot exposes the underlying client (for inline keyboards in later phases).
func (t *Telegram) Bot() *tg.Bot { return t.bot }
