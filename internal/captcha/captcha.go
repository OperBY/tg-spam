package captcha

import (
	"fmt"
	"math/rand"
	"time"
)

type Task struct {
	Question string
	Answer   int
	Options  []string
}

func GenerateTask() Task {
	rand.Seed(time.Now().UnixNano())

	// --- Generating different operands ---
	a, b := genTwoUnique()

	// Correct answer
	answer := a + b

	question := fmt.Sprintf("%d + %d = ?", a, b)

	// --- Generating options ---

	// 1. The correct option
	optCorrect := fmt.Sprintf("%d", answer)

	// 2. Rearranged digits of the correct answer (clearly distinguishable from the answer)
	optReversed := reversedDigits(answer)

	// 3. Number A (first operand)
	optA := fmt.Sprintf("%d", a)

	// 4. Number B (second operand)
	optB := fmt.Sprintf("%d", b)

	// 5. Unique random option
	optRandom := genUniqueRandom([]string{
		optCorrect, optReversed, optA, optB,
	})

	// compiling a complete list (5 unique options guaranteed)
	opts := []string{
		optCorrect,
		optReversed,
		optA,
		optB,
		optRandom,
	}

	shuffle(opts)

	return Task{
		Question: question,
		Answer:   answer,
		Options:  opts,
	}
}

// --- MAIN FUNCTIONS ---

// Generates two different numbers 10–49
func genTwoUnique() (int, int) {
	for {
		a := rand.Intn(40) + 10
		b := rand.Intn(40) + 10
		if a != b {
			return a, b
		}
	}
}

// Rearranges the digits of the result
func reversedDigits(n int) string {
	if n < 10 || n > 99 {
		return fmt.Sprintf("%d", n)
	}

	d1 := n / 10
	d2 := n % 10

	// If the numbers are the same (11, 22...), then we do +1
	if d1 == d2 {
		return fmt.Sprintf("%d", n+1)
	}

	// We swap the tens and units
	return fmt.Sprintf("%d%d", d2, d1)
}

// Generate a random unique options 10–49
func genUniqueRandom(existing []string) string {
	exists := func(x string) bool {
		for _, e := range existing {
			if e == x {
				return true
			}
		}
		return false
	}

	for {
		n := rand.Intn(40) + 10
		s := fmt.Sprintf("%d", n)
		if !exists(s) {
			return s
		}
	}
}

// Mixes up the options
func shuffle(a []string) {
	rand.Seed(time.Now().UnixNano())
	for i := range a {
		j := rand.Intn(i + 1)
		a[i], a[j] = a[j], a[i]
	}
}
