package captcha

import (
	"sync"
	"time"
)

// UserState keeps all information related to the CAPTCHA challenge
// for a single user currently undergoing verification.
type UserState struct {
	ChatID    int64     // Telegram chat ID where verification is required
	UserID    int64     // User ID that must solve the CAPTCHA
	Answer    int       // Correct numeric answer for the current CAPTCHA
	MessageID int       // Telegram message ID containing the CAPTCHA buttons
	Deadline  time.Time // Expiration timestamp for this CAPTCHA
	Attempts  int       // Remaining attempts (e.g., 2 → 1 → 0 → fail)
}

// Manager safely stores CAPTCHA state for all currently challenged users.
type Manager struct {
	m   map[uint64]UserState // Key: stateKey(chatID, userID) → state
	mux sync.RWMutex
}

// NewManager creates a fresh manager with no active states.
func NewManager() *Manager {
	return &Manager{
		m: make(map[uint64]UserState),
	}
}

// Set stores or overwrites a user's CAPTCHA state.
func (mgr *Manager) Set(state UserState) {
	mgr.mux.Lock()
	defer mgr.mux.Unlock()
	mgr.m[stateKey(state.ChatID, state.UserID)] = state
}

// Get retrieves a user's CAPTCHA state if it exists.
func (mgr *Manager) Get(chatID, userID int64) (UserState, bool) {
	mgr.mux.RLock()
	defer mgr.mux.RUnlock()
	st, ok := mgr.m[stateKey(chatID, userID)]
	return st, ok
}

// Delete removes a user's CAPTCHA state (after success or failure).
func (mgr *Manager) Delete(chatID, userID int64) {
	mgr.mux.Lock()
	defer mgr.mux.Unlock()
	delete(mgr.m, stateKey(chatID, userID))
}

// Check verifies if the user has provided the correct answer.
// Expired CAPTCHA is always invalid.
func (mgr *Manager) Check(chatID, userID int64, answer int) bool {
	mgr.mux.RLock()
	defer mgr.mux.RUnlock()

	st, ok := mgr.m[stateKey(chatID, userID)]
	if !ok {
		return false
	}

	// Expired → automatically fail
	if time.Now().After(st.Deadline) {
		return false
	}

	return st.Answer == answer
}

// Expired reports whether the user’s CAPTCHA deadline has passed.
func (mgr *Manager) Expired(chatID, userID int64) bool {
	mgr.mux.RLock()
	defer mgr.mux.RUnlock()

	st, ok := mgr.m[stateKey(chatID, userID)]
	if !ok {
		return true
	}

	return time.Now().After(st.Deadline)
}

// Update replaces the user's state with a modified copy.
// This is useful to modify attempts or regenerate CAPTCHA.
func (mgr *Manager) Update(chatID, userID int64, updateFn func(*UserState)) bool {
	mgr.mux.Lock()
	defer mgr.mux.Unlock()

	st, ok := mgr.m[stateKey(chatID, userID)]
	if !ok {
		return false
	}

	updateFn(&st)
	mgr.m[stateKey(chatID, userID)] = st
	return true
}

func stateKey(chatID, userID int64) uint64 {
	return (uint64(chatID) << 32) | uint64(userID)
}
