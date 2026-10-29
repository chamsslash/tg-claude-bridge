package llm

import (
	"sync"
	"time"
)

// Budget держит число запусков Claude в час ниже порога: каждый запуск
// тратит лимит подписки, и зациклившийся сервис или поток писем не должны
// съесть его целиком.
type Budget struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu    sync.Mutex
	marks []time.Time
}

func NewBudget(max int, window time.Duration, now func() time.Time) *Budget {
	return &Budget{max: max, window: window, now: now}
}

// Allow отвечает, можно ли запустить Claude сейчас, и при «да» сразу
// учитывает запуск.
func (b *Budget) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.trim()
	if len(b.marks) >= b.max {
		return false
	}
	b.marks = append(b.marks, b.now())
	return true
}

func (b *Budget) trim() {
	cutoff := b.now().Add(-b.window)
	i := 0
	for i < len(b.marks) && !b.marks[i].After(cutoff) {
		i++
	}
	b.marks = b.marks[i:]
}
