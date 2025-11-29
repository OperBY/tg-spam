package captcha

import (
	"fmt"
	"strconv"
	"time"

	tele "gopkg.in/telebot.v3"
)

// Handler encapsulates CAPTCHA logic and shared state manager.
type Handler struct {
	Bot     *tele.Bot     // telegram bot instance
	State   *Manager      // manager for active CAPTCHA states
	Timeout time.Duration // lifetime for each CAPTCHA (59s)
}

// NewHandler creates a new CAPTCHA handler.
func NewHandler(bot *tele.Bot, timeout time.Duration) *Handler {
	h := &Handler{
		Bot:     bot,
		State:   NewManager(),
		Timeout: timeout,
	}
	return h
}

// RegisterRoutes attaches handlers to bot events. Call once from main.
func (h *Handler) RegisterRoutes() {
	// New user joined -> send captcha
	h.Bot.Handle(tele.OnUserJoined, func(c tele.Context) error {
		return h.onJoin(c)
	})

	// Delete messages from users who haven't solved captcha yet
	h.Bot.Handle(tele.OnText, h.onMessage)
	h.Bot.Handle(tele.OnPhoto, h.onMessage)
	h.Bot.Handle(tele.OnDocument, h.onMessage)
	h.Bot.Handle(tele.OnSticker, h.onMessage)
	h.Bot.Handle(tele.OnVideo, h.onMessage)

	// Handle inline button callbacks (answers)
	h.Bot.Handle(&tele.Callback{}, func(c tele.Context) error {
		return h.onCallback(c)
	})
}

// ------------------------- JOIN FLOW -------------------------

// onJoin is triggered when a new user joins the chat.
// It:
// - generates a captcha task
// - sends the captcha message with inline buttons
// - restricts the user (no send permissions)
// - stores state with Attempts = 2 and Deadline = now + Timeout
// - schedules an expiration check (timer) that will kick user after deadline
func (h *Handler) onJoin(c tele.Context) error {
	chat := c.Chat()
	// For join event telebot may set Sender() to the joined user.
	user := c.Sender()
	if user == nil {
		// fallback: try to get from message (rare)
		if c.Message() != nil && len(c.Message().NewChatMembers) > 0 {
			user = c.Message().NewChatMembers[0]
		}
		if user == nil {
			return nil
		}
	}

	// create task
	task := GenerateTask()

	// build inline keyboard from options
	var buttons [][]tele.InlineButton
	row := []tele.InlineButton{}
	for _, opt := range task.Options {
		// Use Unique to avoid collisions, Data to carry numeric answer
		btn := tele.InlineButton{
			Unique: "captcha_opt", // same unique is OK because we handle generic callbacks
			Text:   opt,
			Data:   opt,
		}
		row = append(row, btn)
	}
	buttons = append(buttons, row)

	// send captcha message
	msgText := fmt.Sprintf("👋 Welcome, %s!\n\nPlease solve this CAPTCHA to prove you are human:\n\n<b>%s</b>",
		user.FirstName, task.Question)

	msg, err := h.Bot.Send(
		chat,
		msgText,
		&tele.SendOptions{
			ParseMode: tele.ModeHTML,
			ReplyMarkup: &tele.ReplyMarkup{
				InlineKeyboard: buttons,
			},
		},
	)
	if err != nil {
		return err
	}

	// restrict user (no permissions) until they pass captcha
	restrict := tele.ChatPermissions{} // empty => no send rights
	_ = h.Bot.Restrict(chat, user, &restrict)

	// store state with 2 attempts
	h.State.Set(UserState{
		ChatID:    chat.ID,
		UserID:    user.ID,
		Answer:    task.Answer,
		MessageID: msg.ID,
		Deadline:  time.Now().Add(h.Timeout),
		Attempts:  2,
	})

	// schedule expiration check: this goroutine checks actual Deadline before kicking,
	// so if we refresh Deadline later this timer will do nothing.
	go func(uid int64) {
		// wait for timeout duration, then verify current state
		time.Sleep(h.Timeout + 1*time.Second) // small buffer
		st, ok := h.State.Get(uid)
		if !ok {
			return
		}
		// if deadline passed -> fail
		if time.Now().After(st.Deadline) {
			// fetch minimal user info to pass to failCaptcha
			userObj := &tele.User{ID: int(st.UserID)}
			h.failCaptcha(st, userObj, "⏳ CAPTCHA expired — user removed. If you are human, try joining again.")
		}
	}(user.ID)

	return nil
}

// ------------------------- MESSAGE DELETION -------------------------

// onMessage deletes any messages from users who have an active captcha.
func (h *Handler) onMessage(c tele.Context) error {
	user := c.Sender()
	if user == nil {
		return nil
	}
	_, has := h.State.Get(user.ID)
	if has {
		// delete the message (suppress spam while in restricted state)
		_ = h.Bot.Delete(c.Message())
	}
	return nil
}

// ------------------------- CALLBACK HANDLING -------------------------

// onCallback processes pressed inline button answers.
// Logic:
// - check that callback comes from the challenged user
// - parse answer
// - if correct -> passCaptcha
// - if incorrect and Attempts > 1 -> generate new captcha, Attempts--, reset Deadline (59s), replace message (delete old)
// - if incorrect and Attempts == 1 -> failCaptcha (kick)
func (h *Handler) onCallback(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	user := c.Sender()
	if user == nil {
		return c.Respond(&tele.CallbackResponse{Text: "Invalid user."})
	}

	// get current state
	st, ok := h.State.Get(user.ID)
	if !ok {
		return c.Respond(&tele.CallbackResponse{Text: "This CAPTCHA is no longer active.", ShowAlert: false})
	}

	// ensure this callback is for the same chat / message and for the same user
	// only challenged user may answer
	if st.MessageID != cb.Message.ID || st.ChatID != cb.Message.Chat.ID {
		// someone else clicked the button
		return c.Respond(&tele.CallbackResponse{Text: "This CAPTCHA is not for you.", ShowAlert: true})
	}

	// parse pressed value
	ans, err := strconv.Atoi(cb.Data)
	if err != nil {
		// not a number — ignore
		_ = c.Respond()
		return nil
	}

	// check deadline
	if time.Now().After(st.Deadline) {
		// expired - remove
		h.State.Delete(user.ID)
		_ = c.Respond(&tele.CallbackResponse{Text: "CAPTCHA expired."})
		h.failCaptcha(st, &tele.User{ID: int(st.UserID)}, "⏳ CAPTCHA expired — user removed. If you are human, try joining again.")
		return nil
	}

	// correct answer
	if ans == st.Answer {
		// acknowledge callback (silent)
		_ = c.Respond(&tele.CallbackResponse{Text: "Correct!"})
		h.passCaptcha(st, &tele.User{ID: int(st.UserID)})
		return nil
	}

	// wrong answer
	// decrement attempts (we stored initial Attempts == 2)
	if st.Attempts <= 1 {
		// final attempt used -> fail
		_ = c.Respond(&tele.CallbackResponse{Text: "Wrong. No attempts left."})
		h.failCaptcha(st, &tele.User{ID: int(st.UserID)}, "❌ CAPTCHA failed twice — user removed. If you are human, try joining again.")
		return nil
	}

	// st.Attempts >= 2 -> first wrong answer case:
	// generate a NEW captcha, decrement attempts, update state, delete old captcha message,
	// send new captcha message and notify user that this is last attempt.

	// decrease attempts
	newAttempts := st.Attempts - 1

	// delete old captcha message (best-effort)
	_ = h.Bot.Delete(&tele.Message{ID: st.MessageID, Chat: &tele.Chat{ID: st.ChatID}})

	// create new task
	task := GenerateTask()

	// build keyboard
	var buttons [][]tele.InlineButton
	row := []tele.InlineButton{}
	for _, opt := range task.Options {
		btn := tele.InlineButton{
			Unique: "captcha_opt",
			Text:   opt,
			Data:   opt,
		}
		row = append(row, btn)
	}
	buttons = append(buttons, row)

	// send new captcha; include last-attempt warning
	newMsgText := fmt.Sprintf("⚠ %s, this is your <b>last attempt</b>.\n\nPlease solve:\n\n<b>%s</b>",
		user.FirstName, task.Question)

	newMsg, err := h.Bot.Send(
		&tele.Chat{ID: st.ChatID},
		newMsgText,
		&tele.SendOptions{
			ParseMode: tele.ModeHTML,
			ReplyMarkup: &tele.ReplyMarkup{
				InlineKeyboard: buttons,
			},
		},
	)
	if err != nil {
		// if we cannot send new message, fail safe by kicking user
		h.failCaptcha(st, &tele.User{ID: int(st.UserID)}, "❌ Error sending CAPTCHA — user removed.")
		return nil
	}

	// update stored state: new answer, new message id, new deadline, decreased attempts
	newState := UserState{
		ChatID:    st.ChatID,
		UserID:    st.UserID,
		Answer:    task.Answer,
		MessageID: newMsg.ID,
		Deadline:  time.Now().Add(h.Timeout),
		Attempts:  newAttempts,
	}
	h.State.Set(newState)

	// schedule a new expiration checker for this user (previous timers check Deadline so are harmless)
	go func(uid int64) {
		time.Sleep(h.Timeout + 1*time.Second)
		cur, ok := h.State.Get(uid)
		if !ok {
			return
		}
		if time.Now().After(cur.Deadline) {
			h.failCaptcha(cur, &tele.User{ID: int(cur.UserID)}, "⏳ CAPTCHA expired — user removed. If you are human, try joining again.")
		}
	}(st.UserID)

	// respond to callback (small popup)
	_ = c.Respond(&tele.CallbackResponse{Text: "Wrong answer. New CAPTCHA sent. This is your last attempt.", ShowAlert: false})

	return nil
}

// ------------------------- PASS / FAIL HELPERS -------------------------

// passCaptcha is called when a user successfully solves the CAPTCHA.
// It:
// - restores send permissions
// - deletes the captcha message
// - removes state
// - optionally notifies the chat
func (h *Handler) passCaptcha(st UserState, user *tele.User) {
	chat := &tele.Chat{ID: st.ChatID}

	// allow normal permissions
	allow := tele.ChatPermissions{
		CanSendMessages: true,
		CanSendMedia:    true,
		CanSendOther:    true,
		CanAddPreviews:  true,
	}
	_ = h.Bot.Restrict(chat, user, &allow)

	// delete captcha message (best-effort)
	_ = h.Bot.Delete(&tele.Message{ID: st.MessageID, Chat: chat})

	// remove internal state
	h.State.Delete(user.ID)

	// optional friendly message
	_, _ = h.Bot.Send(chat, fmt.Sprintf("✅ %s passed the CAPTCHA.", user.FirstName))
}

// failCaptcha removes the user from the chat and cleans state.
// reason is sent to chat for transparency.
func (h *Handler) failCaptcha(st UserState, user *tele.User, reason string) {
	chat := &tele.Chat{ID: st.ChatID}

	// delete captcha message if exists
	_ = h.Bot.Delete(&tele.Message{ID: st.MessageID, Chat: chat})

	// delete state
	h.State.Delete(user.ID)

	// ban (kick) the user
	_ = h.Bot.Ban(chat, user)

	// notify chat with human-friendly text
	_, _ = h.Bot.Send(chat, reason)
}
