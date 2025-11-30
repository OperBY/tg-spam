package captcha

import (
	"testing"
	"time"
)

func TestManagerStateKeyNoCollisionForLargeChatIDs(t *testing.T) {
	mgr := NewManager()

	chatHighBits := int64(1<<40 + 123)
	chatLowBits := int64(123)
	userID := int64(42)

	mgr.Set(UserState{ChatID: chatHighBits, UserID: userID, Answer: 1, Deadline: time.Now().Add(time.Hour)})
	mgr.Set(UserState{ChatID: chatLowBits, UserID: userID, Answer: 2, Deadline: time.Now().Add(time.Hour)})

	stHigh, ok := mgr.Get(chatHighBits, userID)
	if !ok {
		t.Fatalf("expected state for chat %d", chatHighBits)
	}
	if stHigh.Answer != 1 {
		t.Fatalf("expected answer 1 for chat %d, got %d", chatHighBits, stHigh.Answer)
	}

	stLow, ok := mgr.Get(chatLowBits, userID)
	if !ok {
		t.Fatalf("expected state for chat %d", chatLowBits)
	}
	if stLow.Answer != 2 {
		t.Fatalf("expected answer 2 for chat %d, got %d", chatLowBits, stLow.Answer)
	}
}
