package bot

import (
	"context"
	"errors"
	"io"
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
	// OnCallback handles inline-button data; the returned reply replaces the
	// message that carried the buttons (text and keyboard). Optional.
	OnCallback func(ctx context.Context, data string) Reply
	// OnText handles non-command text. Optional.
	OnText func(ctx context.Context, text string) Reply
	// OnCommand handles commands whose reply needs a keyboard (/cal, /log).
	// handled=false falls back to the plain Commands. Optional.
	OnCommand func(ctx context.Context, cmd, args string) (r Reply, handled bool)
}

// Button is an inline keyboard button.
type Button struct {
	Text string
	Data string // callback data (≤ 64 bytes)
}

// Reply is a message with optional inline buttons: Buttons is one row,
// Rows a full keyboard (Rows wins when both are set).
type Reply struct {
	Text    string
	Buttons []Button
	Rows    [][]Button
	// Callbacks only: Toast is shown as a short notice; Keep leaves the
	// message as it is (only the toast).
	Toast string
	Keep  bool
}

// keyboard builds the inline keyboard of r (nil when it has none).
func (r Reply) keyboard() *models.InlineKeyboardMarkup {
	rows := r.Rows
	if len(rows) == 0 && len(r.Buttons) > 0 {
		rows = [][]Button{r.Buttons}
	}
	if len(rows) == 0 {
		return nil
	}
	kb := make([][]models.InlineKeyboardButton, 0, len(rows))
	for _, row := range rows {
		out := make([]models.InlineKeyboardButton, 0, len(row))
		for _, b := range row {
			out = append(out, models.InlineKeyboardButton{Text: b.Text, CallbackData: b.Data})
		}
		kb = append(kb, out)
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: kb}
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
	chatID := u.Message.Chat.ID
	// Natural language and /tanya, /cari wait on the LLM or the web: show
	// "typing..." until the reply is ready.
	stopTyping := keepTyping(ctx, func(ctx context.Context) {
		if _, err := b.SendChatAction(ctx, &tg.SendChatActionParams{ChatID: chatID, Action: models.ChatActionTyping}); err != nil && ctx.Err() == nil {
			t.log.Debug("sendChatAction failed", "err", t.Redact(err))
		}
	}, typingFirst, typingEvery)
	var reply Reply
	cmd, args, isCmd := SplitCommand(text)
	handled := false
	switch {
	case !isCmd && t.OnText != nil:
		reply, handled = t.OnText(ctx, text), true
	case isCmd && t.OnCommand != nil:
		reply, handled = t.OnCommand(ctx, cmd, args)
	}
	if !handled {
		reply = Reply{Text: t.cmds.Handle(ctx, text)}
	}
	stopTyping()
	if reply.Text == "" {
		return
	}
	if err := t.SendReply(ctx, chatID, reply); err != nil {
		t.log.Warn("reply failed", "err", err)
	}
}

func (t *Telegram) handleCallback(ctx context.Context, b *tg.Bot, q *models.CallbackQuery) {
	var r Reply
	if t.OnCallback != nil && q.Message.Message != nil {
		r = t.OnCallback(ctx, q.Data)
	}
	if _, err := b.AnswerCallbackQuery(ctx, &tg.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: r.Toast}); err != nil {
		t.log.Warn("answerCallbackQuery failed", "err", t.Redact(err))
	}
	if r.Keep || r.Text == "" || q.Message.Message == nil {
		return
	}
	msg := q.Message.Message
	if err := t.EditReply(ctx, msg.Chat.ID, msg.ID, r); err != nil {
		t.log.Warn("editMessageText failed", "err", err)
	}
}

// EditReply replaces a message's text and keyboard (no keyboard = removed).
func (t *Telegram) EditReply(ctx context.Context, chatID int64, msgID int, r Reply) error {
	p := &tg.EditMessageTextParams{ChatID: chatID, MessageID: msgID, Text: r.Text}
	if kb := r.keyboard(); kb != nil {
		p.ReplyMarkup = kb
	} else {
		p.ReplyMarkup = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}
	}
	if _, err := t.bot.EditMessageText(ctx, p); err != nil {
		if strings.Contains(err.Error(), "message is not modified") {
			return nil // same calendar again: nothing to do
		}
		return errors.New(t.Redact(err))
	}
	return nil
}

// DeleteMessage removes a message (old prompts, so the chat does not pile up).
func (t *Telegram) DeleteMessage(ctx context.Context, chatID int64, msgID int) error {
	if _, err := t.bot.DeleteMessage(ctx, &tg.DeleteMessageParams{ChatID: chatID, MessageID: msgID}); err != nil {
		return errors.New(t.Redact(err))
	}
	return nil
}

// SendReply sends a message with optional inline buttons.
func (t *Telegram) SendReply(ctx context.Context, chatID int64, r Reply) error {
	_, err := t.SendReplyID(ctx, chatID, r)
	return err
}

// SendReplyID sends a message and returns its id (to edit it later).
func (t *Telegram) SendReplyID(ctx context.Context, chatID int64, r Reply) (int, error) {
	p := &tg.SendMessageParams{ChatID: chatID, Text: r.Text}
	if kb := r.keyboard(); kb != nil {
		p.ReplyMarkup = kb
	}
	m, err := t.bot.SendMessage(ctx, p)
	if err != nil {
		return 0, errors.New(t.Redact(err))
	}
	return m.ID, nil
}

// OwnerID is the owner's chat id (private chat).
func (t *Telegram) OwnerID() int64 { return t.ownerID }

// Send sends a plain-text message.
func (t *Telegram) Send(ctx context.Context, chatID int64, text string) error {
	return t.SendReply(ctx, chatID, Reply{Text: text})
}

// NotifyOwner sends a message to the owner's private chat.
func (t *Telegram) NotifyOwner(ctx context.Context, text string) error {
	return t.Send(ctx, t.ownerID, text)
}

// SendDocument uploads a file to the owner's chat.
func (t *Telegram) SendDocument(ctx context.Context, filename string, data io.Reader, caption string) error {
	_, err := t.bot.SendDocument(ctx, &tg.SendDocumentParams{
		ChatID:   t.ownerID,
		Document: &models.InputFileUpload{Filename: filename, Data: data},
		Caption:  caption,
	})
	if err != nil {
		return errors.New(t.Redact(err))
	}
	return nil
}
