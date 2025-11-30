package captcha

import (
	"fmt"
	"strconv"
	"time"

	tbapi "github.com/OvyFlash/telegram-bot-api"
)

// BotAPI is a minimal interface required from telegram API client.
// It matches *tbapi.BotAPI from github.com/OvyFlash/telegram-bot-api.
type BotAPI interface {
	Send(c tbapi.Chattable) (tbapi.Message, error)
	Request(c tbapi.Chattable) (*tbapi.APIResponse, error)
}

// Handler encapsulates CAPTCHA logic and shared state manager.
type Handler struct {
	Bot     BotAPI        // telegram bot instance
	State   *Manager      // manager for active CAPTCHA states
	Timeout time.Duration // lifetime for each CAPTCHA (59s)
}

// NewHandler creates a new CAPTCHA handler.
func NewHandler(bot BotAPI, timeout time.Duration) *Handler {
	h := &Handler{
		Bot:     bot,
		State:   NewManager(),
		Timeout: timeout,
	}
	return h
}

// OnUserJoined is triggered when a new user joins the chat.
// It performs the following steps:
//   - generates a captcha task and sends it with inline buttons
//   - restricts the user (no send permissions)
//   - stores state with Attempts = 2 and Deadline = now + Timeout
//   - schedules an expiration check that will kick user after deadline
func (h *Handler) OnUserJoined(update *tbapi.ChatMemberUpdated) error {
	if update == nil || update.NewChatMember.User == nil {
		return nil
	}

	user := update.NewChatMember.User

	// create task
	task := GenerateTask()

	// build inline keyboard from options
	var buttons [][]tbapi.InlineKeyboardButton
	row := []tbapi.InlineKeyboardButton{}
	for _, opt := range task.Options {
		btn := tbapi.NewInlineKeyboardButtonData(opt, opt)
		row = append(row, btn)
	}
	buttons = append(buttons, row)

	// send captcha message
	msgText := fmt.Sprintf("👋 Welcome, %s!\n\nPlease solve this CAPTCHA to prove you are human:\n\n<b>%s</b>",
		user.FirstName, task.Question)

	captchaMsg, err := h.Bot.Send(tbapi.MessageConfig{
		BaseChat: tbapi.BaseChat{ChatConfig: tbapi.ChatConfig{ChatID: update.Chat.ID}, ReplyMarkup: &tbapi.InlineKeyboardMarkup{
			InlineKeyboard: buttons,
		}},
		Text:      msgText,
		ParseMode: tbapi.ModeHTML,
	})
	if err != nil {
		return err
	}

	// restrict user (no permissions) until they pass captcha
	_, _ = h.Bot.Request(tbapi.RestrictChatMemberConfig{
		ChatMemberConfig: tbapi.ChatMemberConfig{
			ChatConfig: tbapi.ChatConfig{ChatID: update.Chat.ID},
			UserID:     user.ID,
		},
		Permissions: &tbapi.ChatPermissions{},
	})

	// store state with 2 attempts
	h.State.Set(UserState{
		ChatID:    update.Chat.ID,
		UserID:    user.ID,
		Answer:    task.Answer,
		MessageID: captchaMsg.MessageID,
		Deadline:  time.Now().Add(h.Timeout),
		Attempts:  2,
	})

	// schedule expiration check
	go func(chatID, uid int64) {
		time.Sleep(h.Timeout + 1*time.Second)
		st, ok := h.State.Get(chatID, uid)
		if !ok {
			return
		}
		if time.Now().After(st.Deadline) {
			h.failCaptcha(st, user.FirstName, "⏳ CAPTCHA expired — user removed. If you are human, try joining again.")
		}
	}(update.Chat.ID, user.ID)

	return nil
}

// OnMessage deletes any messages from users who have an active captcha.
// Returns true if the message was handled (deleted) and should not be processed further.
func (h *Handler) OnMessage(msg *tbapi.Message) bool {
	if msg == nil || msg.From == nil {
		return false
	}
	_, has := h.State.Get(msg.Chat.ID, msg.From.ID)
	if has {
		_, _ = h.Bot.Request(tbapi.DeleteMessageConfig{ // best-effort
			BaseChatMessage: tbapi.BaseChatMessage{ChatConfig: tbapi.ChatConfig{ChatID: msg.Chat.ID}, MessageID: msg.MessageID},
		})
		return true
	}
	return false
}

// OnCallback processes pressed inline button answers.
// Returns true if the callback was processed by CAPTCHA module.
func (h *Handler) OnCallback(query *tbapi.CallbackQuery) bool {
	if query == nil || query.From == nil || query.Message == nil {
		return false
	}

	st, ok := h.State.Get(query.Message.Chat.ID, query.From.ID)
	if !ok {
		return false
	}

	// ensure this callback is for the same chat / message and for the same user
	if st.MessageID != query.Message.MessageID || st.ChatID != query.Message.Chat.ID {
		h.answerCallback(query.ID, "This CAPTCHA is not for you.", true)
		return true
	}

	ans, err := strconv.Atoi(query.Data)
	if err != nil {
		h.answerCallback(query.ID, "Invalid answer.", false)
		return true
	}

	if time.Now().After(st.Deadline) {
		h.State.Delete(query.Message.Chat.ID, query.From.ID)
		h.answerCallback(query.ID, "CAPTCHA expired.", false)
		h.failCaptcha(st, query.From.FirstName, "⏳ CAPTCHA expired — user removed. If you are human, try joining again.")
		return true
	}

	if ans == st.Answer {
		h.answerCallback(query.ID, "Correct!", false)
		h.passCaptcha(st, query.From.FirstName)
		return true
	}

	if st.Attempts <= 1 {
		h.answerCallback(query.ID, "Wrong. No attempts left.", false)
		h.failCaptcha(st, query.From.FirstName, "❌ CAPTCHA failed twice — user removed. If you are human, try joining again.")
		return true
	}

	// first wrong answer, regenerate captcha
	newAttempts := st.Attempts - 1

	// delete old captcha message
	_, _ = h.Bot.Request(tbapi.DeleteMessageConfig{BaseChatMessage: tbapi.BaseChatMessage{ChatConfig: tbapi.ChatConfig{ChatID: st.ChatID}, MessageID: st.MessageID}})

	task := GenerateTask()

	var buttons [][]tbapi.InlineKeyboardButton
	row := []tbapi.InlineKeyboardButton{}
	for _, opt := range task.Options {
		btn := tbapi.NewInlineKeyboardButtonData(opt, opt)
		row = append(row, btn)
	}
	buttons = append(buttons, row)

	newMsgText := fmt.Sprintf("⚠ %s, this is your <b>last attempt</b>.\n\nPlease solve:\n\n<b>%s</b>",
		query.From.FirstName, task.Question)

	newMsg, err := h.Bot.Send(tbapi.MessageConfig{
		BaseChat: tbapi.BaseChat{ChatConfig: tbapi.ChatConfig{ChatID: st.ChatID}, ReplyMarkup: &tbapi.InlineKeyboardMarkup{
			InlineKeyboard: buttons,
		}},
		Text:      newMsgText,
		ParseMode: tbapi.ModeHTML,
	})
	if err != nil {
		h.failCaptcha(st, query.From.FirstName, "❌ Error sending CAPTCHA — user removed.")
		return true
	}

	h.State.Set(UserState{
		ChatID:    st.ChatID,
		UserID:    st.UserID,
		Answer:    task.Answer,
		MessageID: newMsg.MessageID,
		Deadline:  time.Now().Add(h.Timeout),
		Attempts:  newAttempts,
	})

	go func(chatID, uid int64) {
		time.Sleep(h.Timeout + 1*time.Second)
		cur, ok := h.State.Get(chatID, uid)
		if !ok {
			return
		}
		if time.Now().After(cur.Deadline) {
			h.failCaptcha(cur, query.From.FirstName, "⏳ CAPTCHA expired — user removed. If you are human, try joining again.")
		}
	}(st.ChatID, st.UserID)

	h.answerCallback(query.ID, "Wrong answer. New CAPTCHA sent. This is your last attempt.", false)

	return true
}

func (h *Handler) answerCallback(id, text string, alert bool) {
	_, _ = h.Bot.Request(tbapi.CallbackConfig{CallbackQueryID: id, Text: text, ShowAlert: alert})
}

// passCaptcha is called when a user successfully solves the CAPTCHA.
// It restores permissions, deletes captcha message, removes state and notifies chat.
func (h *Handler) passCaptcha(st UserState, firstName string) {
	chatID := st.ChatID

	_, _ = h.Bot.Request(tbapi.RestrictChatMemberConfig{
		ChatMemberConfig: tbapi.ChatMemberConfig{ChatConfig: tbapi.ChatConfig{ChatID: chatID}, UserID: st.UserID},
		Permissions: &tbapi.ChatPermissions{
			CanSendMessages:       true,
			CanSendAudios:         true,
			CanSendDocuments:      true,
			CanSendPhotos:         true,
			CanSendVideos:         true,
			CanSendVideoNotes:     true,
			CanSendVoiceNotes:     true,
			CanSendOtherMessages:  true,
			CanAddWebPagePreviews: true,
		},
	})

	_, _ = h.Bot.Request(tbapi.DeleteMessageConfig{BaseChatMessage: tbapi.BaseChatMessage{ChatConfig: tbapi.ChatConfig{ChatID: chatID}, MessageID: st.MessageID}})

	h.State.Delete(st.ChatID, st.UserID)

	msg, _ := h.Bot.Send(tbapi.NewMessage(chatID, fmt.Sprintf("✅ %s passed the CAPTCHA.", firstName)))
	h.scheduleDelete(chatID, msg.MessageID, time.Minute)
}

// failCaptcha removes the user from the chat and cleans state.
func (h *Handler) failCaptcha(st UserState, firstName, reason string) {
	chatID := st.ChatID

	_, _ = h.Bot.Request(tbapi.DeleteMessageConfig{BaseChatMessage: tbapi.BaseChatMessage{ChatConfig: tbapi.ChatConfig{ChatID: chatID}, MessageID: st.MessageID}})

	h.State.Delete(st.ChatID, st.UserID)

	_, _ = h.Bot.Request(tbapi.BanChatMemberConfig{
		ChatMemberConfig: tbapi.ChatMemberConfig{ChatConfig: tbapi.ChatConfig{ChatID: chatID}, UserID: st.UserID},
		RevokeMessages:   true,
	})

	msg, _ := h.Bot.Send(tbapi.NewMessage(chatID, reason))
	h.scheduleDelete(chatID, msg.MessageID, time.Minute)
}

// scheduleDelete removes a bot message after the given delay.
func (h *Handler) scheduleDelete(chatID int64, messageID int, delay time.Duration) {
	time.AfterFunc(delay, func() {
		_, _ = h.Bot.Request(tbapi.DeleteMessageConfig{BaseChatMessage: tbapi.BaseChatMessage{ChatConfig: tbapi.ChatConfig{ChatID: chatID}, MessageID: messageID}})
	})
}
