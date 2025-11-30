package captcha

import (
	"math/rand"
	"strconv"
	"sync"
	"time"
)

// Manager tracks captcha states per chat/user.
// The manager is goroutine-safe.
type Manager struct {
	timeout   time.Duration
	onTimeout func(chatID, userID int64)

	rand *rand.Rand

	mu sync.Mutex

	states map[stateKey]*state
}

// ResultStatus defines the result of captcha verification.
type ResultStatus int

const (
	ResultUnknown ResultStatus = iota
	ResultSuccess
	ResultRetry
	ResultFailed
	ResultNotFound
)

// Result represents verification status.
type Result struct {
	Status       ResultStatus
	AttemptsLeft int
	Messages     []int
}

// Challenge describes captcha task.
type Challenge struct {
	A        int
	B        int
	Question string
	Options  []int
	Answer   int
}

type stateKey struct {
	chatID int64
	userID int64
}

type state struct {
	attemptsLeft int
	answer       int
	challenge    Challenge
	timer        *time.Timer
	startedAt    time.Time
	messages     []int
}

// New creates captcha manager.
func New(timeout time.Duration, onTimeout func(chatID, userID int64)) *Manager {
	return &Manager{
		timeout:   timeout,
		onTimeout: onTimeout,
		rand:      rand.New(rand.NewSource(time.Now().UnixNano())),
		states:    map[stateKey]*state{},
	}
}

// Start creates new challenge for chat/user replacing existing state.
func (m *Manager) Start(chatID, userID int64) Challenge {
	key := stateKey{chatID: chatID, userID: userID}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.stop(key)

	ch := m.generateChallenge()
	st := &state{attemptsLeft: 2, answer: ch.Answer, challenge: ch, startedAt: time.Now()}
	st.timer = m.newTimer(key)
	m.states[key] = st
	return ch
}

// Active returns true if captcha is pending.
func (m *Manager) Active(chatID, userID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.states[stateKey{chatID: chatID, userID: userID}]
	return ok
}

// Clear removes state and returns tracked message IDs.
func (m *Manager) Clear(chatID, userID int64) []int {
	key := stateKey{chatID: chatID, userID: userID}
	m.mu.Lock()
	defer m.mu.Unlock()

	st := m.clearLocked(key)
	return collectMessages(st)
}

// Verify checks answer and returns next status.
func (m *Manager) Verify(chatID, userID int64, answer int) (Result, *Challenge) {
	key := stateKey{chatID: chatID, userID: userID}
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.states[key]
	if !ok {
		return Result{Status: ResultNotFound}, nil
	}

	if answer == st.answer {
		removed := m.clearLocked(key)
		return Result{Status: ResultSuccess, AttemptsLeft: st.attemptsLeft, Messages: collectMessages(removed)}, nil
	}

	st.attemptsLeft--
	if st.attemptsLeft <= 0 {
		removed := m.clearLocked(key)
		return Result{Status: ResultFailed, AttemptsLeft: 0, Messages: collectMessages(removed)}, nil
	}

	challenge := m.generateChallenge()
	st.answer = challenge.Answer
	st.challenge = challenge
	st.startedAt = time.Now()
	st.timer = m.newTimer(key)
	m.states[key] = st
	return Result{Status: ResultRetry, AttemptsLeft: st.attemptsLeft}, &challenge
}

// TrackMessage adds message ID for later cleanup.
func (m *Manager) TrackMessage(chatID, userID int64, msgID int) {
	key := stateKey{chatID: chatID, userID: userID}
	m.mu.Lock()
	defer m.mu.Unlock()

	st, ok := m.states[key]
	if !ok {
		return
	}
	st.messages = append(st.messages, msgID)
	m.states[key] = st
}

// ChallengeInfo returns current challenge details if active.
func (m *Manager) ChallengeInfo(chatID, userID int64) (Challenge, bool) {
	key := stateKey{chatID: chatID, userID: userID}
	m.mu.Lock()
	defer m.mu.Unlock()

	st, ok := m.states[key]
	if !ok {
		return Challenge{}, false
	}

	return st.challenge, true
}

// StartedAt returns start time of current captcha.
func (m *Manager) StartedAt(chatID, userID int64) (time.Time, bool) {
	key := stateKey{chatID: chatID, userID: userID}
	m.mu.Lock()
	defer m.mu.Unlock()

	st, ok := m.states[key]
	if !ok {
		return time.Time{}, false
	}
	return st.startedAt, true
}

func (m *Manager) stop(key stateKey) {
	st, ok := m.states[key]
	if ok && st.timer != nil {
		st.timer.Stop()
	}
}

func (m *Manager) clearLocked(key stateKey) *state {
	st, ok := m.states[key]
	if !ok {
		return nil
	}
	m.stop(key)
	delete(m.states, key)
	return st
}

func collectMessages(st *state) []int {
	if st == nil {
		return nil
	}
	return append([]int(nil), st.messages...)
}

func (m *Manager) newTimer(key stateKey) *time.Timer {
	chatID, userID := key.chatID, key.userID
	if st, ok := m.states[key]; ok && st.timer != nil {
		st.timer.Stop()
	}
	return time.AfterFunc(m.timeout, func() {
		if m.onTimeout != nil {
			m.onTimeout(chatID, userID)
		}
		m.Clear(chatID, userID)
	})
}

func (m *Manager) generateChallenge() Challenge {
	a, b := m.randomOperand(), m.randomOperand()
	for a == b {
		b = m.randomOperand()
	}

	answer := a + b
	question := strconv.Itoa(a) + " + " + strconv.Itoa(b)
	options := m.generateOptions(answer, a, b)
	return Challenge{A: a, B: b, Question: question, Options: options, Answer: answer}
}

func (m *Manager) randomOperand() int {
	return 10 + m.rand.Intn(40) // 10-49
}

func (m *Manager) generateOptions(answer, a, b int) []int {
	used := map[int]struct{}{answer: {}}

	permutedBase := answer
	permuted := swapDigits(permutedBase)
	if _, exists := used[permuted]; exists {
		permutedBase++
		permuted = swapDigits(permutedBase)
	}
	used[permuted] = struct{}{}

	opt2 := combineDigitsDeterministic(a, b)
	if _, exists := used[opt2]; exists {
		opt2 = mixDigits(a, b)
	}
	used[opt2] = struct{}{}

	opt3 := digitDistanceOption(a, b, answer)
	if _, exists := used[opt3]; exists {
		opt3 = (answer % 10 * 10) + (a % 10)
	}
	used[opt3] = struct{}{}

	extra := 0
	for {
		extra = 10 + m.rand.Intn(90)
		if _, ok := used[extra]; !ok {
			used[extra] = struct{}{}
			break
		}
	}

	opts := []int{answer, permuted, opt2, opt3, extra}
	m.rand.Shuffle(len(opts), func(i, j int) { opts[i], opts[j] = opts[j], opts[i] })
	return opts
}

func (m *Manager) permutedAnswer(answer int) int {
	digits := extractDigits(answer + 0)
	if len(digits) < 2 {
		return answer
	}
	m.rand.Shuffle(len(digits), func(i, j int) { digits[i], digits[j] = digits[j], digits[i] })
	return digits[0]*10 + digits[1]
}

func swapDigits(number int) int {
	digits := extractDigits(number)
	if len(digits) < 2 {
		return number
	}
	return digits[1]*10 + digits[0]
}

func combineDigitsDeterministic(a, b int) int {
	aDigits := extractDigits(a)
	bDigits := extractDigits(b)
	val := aDigits[0]*10 + bDigits[1]
	if val < 10 {
		val = 10 + (aDigits[1]+bDigits[0])%90
	}
	return val
}

func mixDigits(a, b int) int {
	aDigits := extractDigits(a)
	bDigits := extractDigits(b)
	return bDigits[0]*10 + aDigits[1]
}

func digitDistanceOption(a, b, answer int) int {
	diff := absInt(a - b)
	if diff < 10 {
		diff = 10 + diff
	}
	digits := extractDigits(answer)
	if diff < 10 {
		diff = 10 + digits[1]
	}
	return diff%90 + 10
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func extractDigits(number int) []int {
	return []int{(number / 10) % 10, number % 10}
}
