package llm

import (
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestBudgetCaps(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)}
	b := NewBudget(2, time.Hour, c.now)

	if !b.Allow() || !b.Allow() {
		t.Fatal("первые два пробуждения должны пройти")
	}
	if b.Allow() {
		t.Fatal("третье пробуждение в том же часе должно упереться в бюджет")
	}
	c.t = c.t.Add(61 * time.Minute)
	if !b.Allow() {
		t.Fatal("после выхода из окна бюджет должен освободиться")
	}
}
