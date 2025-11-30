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
	timer        *time.Timer
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
	st := &state{attemptsLeft: 2, answer: ch.Answer}
	st.timer = time.AfterFunc(m.timeout, func() {
		if m.onTimeout != nil {
			m.onTimeout(chatID, userID)
		}
		m.Clear(chatID, userID)
	})
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

// Clear removes state.
func (m *Manager) Clear(chatID, userID int64) {
	key := stateKey{chatID: chatID, userID: userID}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.stop(key)
	delete(m.states, key)
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
		m.Clear(chatID, userID)
		return Result{Status: ResultSuccess, AttemptsLeft: st.attemptsLeft}, nil
	}

	st.attemptsLeft--
	if st.attemptsLeft <= 0 {
		m.Clear(chatID, userID)
		return Result{Status: ResultFailed, AttemptsLeft: 0}, nil
	}

	challenge := m.generateChallenge()
	st.answer = challenge.Answer
	m.states[key] = st
	return Result{Status: ResultRetry, AttemptsLeft: st.attemptsLeft}, &challenge
}

func (m *Manager) stop(key stateKey) {
	st, ok := m.states[key]
	if ok && st.timer != nil {
		st.timer.Stop()
	}
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

	permuted := m.permutedAnswer(answer)
	if _, exists := used[permuted]; exists {
		permuted = m.permutedAnswer(answer + 1)
	}
	used[permuted] = struct{}{}

	digits := append(extractDigits(a), extractDigits(b)...)
	digits = append(digits, extractDigits(answer)...)

	addOption := func() int {
		for {
			val := buildFromDigits(digits, m.rand)
			if _, ok := used[val]; !ok {
				used[val] = struct{}{}
				return val
			}
		}
	}

	opt2 := addOption()
	opt3 := addOption()

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

func extractDigits(number int) []int {
	return []int{(number / 10) % 10, number % 10}
}

func buildFromDigits(digits []int, rnd *rand.Rand) int {
	d1 := digits[rnd.Intn(len(digits))]
	d2 := digits[rnd.Intn(len(digits))]
	if d1 == 0 {
		d1 = 1 + rnd.Intn(9)
	}
	return d1*10 + d2
}
