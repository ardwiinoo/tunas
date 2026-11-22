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
	// OnCallback handles inline-button data; the returned text replaces the
	// message that carried the buttons. Optional.
	OnCallback func(ctx context.Context, data string) string
	// OnText handles non-command text. Optional.
	OnText func(ctx context.Context, text string) Reply
}

// Button is an inline keyboard button.
type Button struct {
	Text string
	Data string // callback data (≤ 64 bytes)
}

// Reply is a message with optional inline buttons (one row).
type Reply struct {
	Text    string
	Buttons []Button
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
	if q := u.CallbackQuery; q != nil {
		t.handleCallback(ctx, b, q)
		return
	}
	text := u.Message.Text
	if text == "" {
		return
	}
	var reply Reply
	if _, _, isCmd := SplitCommand(text); !isCmd && t.OnText != nil {
		reply = t.OnText(ctx, text)
	} else {
		reply = Reply{Text: t.cmds.Handle(ctx, text)}
	}
	if reply.Text == "" {
		return
	}
	if err := t.SendReply(ctx, u.Message.Chat.ID, reply); err != nil {
		t.log.Warn("reply failed", "err", err)
	}
}

func (t *Telegram) handleCallback(ctx context.Context, b *tg.Bot, q *models.CallbackQuery) {
	if _, err := b.AnswerCallbackQuery(ctx, &tg.AnswerCallbackQueryParams{CallbackQueryID: q.ID}); err != nil {
		t.log.Warn("answerCallbackQuery failed", "err", t.Redact(err))
	}
	if t.OnCallback == nil || q.Message.Message == nil {
		return
	}
	text := t.OnCallback(ctx, q.Data)
	if text == "" {
		return
	}
	msg := q.Message.Message
	if _, err := b.EditMessageText(ctx, &tg.EditMessageTextParams{
		ChatID: msg.Chat.ID, MessageID: msg.ID, Text: text,
	}); err != nil {
		t.log.Warn("editMessageText failed", "err", t.Redact(err))
	}
}

// SendReply sends a message with optional inline buttons.
func (t *Telegram) SendReply(ctx context.Context, chatID int64, r Reply) error {
	p := &tg.SendMessageParams{ChatID: chatID, Text: r.Text}
	if len(r.Buttons) > 0 {
		row := make([]models.InlineKeyboardButton, 0, len(r.Buttons))
		for _, btn := range r.Buttons {
			row = append(row, models.InlineKeyboardButton{Text: btn.Text, CallbackData: btn.Data})
		}
		p.ReplyMarkup = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{row}}
	}
	if _, err := t.bot.SendMessage(ctx, p); err != nil {
		return errors.New(t.Redact(err))
	}
	return nil
}

// Send sends a plain-text message.
func (t *Telegram) Send(ctx context.Context, chatID int64, text string) error {
	return t.SendReply(ctx, chatID, Reply{Text: text})
}

// NotifyOwner sends a message to the owner's private chat.
func (t *Telegram) NotifyOwner(ctx context.Context, text string) error {
	return t.Send(ctx, t.ownerID, text)
}
