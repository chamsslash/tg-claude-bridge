package health

import (
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// 10:00 UTC — это 13:00 по Москве.
var t0 = time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)

func TestAlertAfterDownAfter(t *testing.T) {
	c := &clock{t: t0}
	m := New(c.now, 6*time.Hour)
	m.Failure("Gmail")

	c.t = t0.Add(5 * time.Hour)
	m.Failure("Gmail")
	if a := m.Alerts(); len(a) != 0 {
		t.Fatalf("5 часов сбоя — ещё не повод писать владельцу: %q", a)
	}

	c.t = t0.Add(6*time.Hour + time.Minute)
	a := m.Alerts()
	if len(a) != 1 || !strings.Contains(a[0], "Gmail") || !strings.Contains(a[0], "13:00 26.10") {
		t.Fatalf("ожидал одно сообщение о сбое с московским временем начала, получено %q", a)
	}
	if !m.Snapshot()["Gmail"].Down {
		t.Fatal("после сообщения о сбое источник должен числиться лежащим")
	}
	if a := m.Alerts(); len(a) != 0 {
		t.Fatalf("о том же сбое второй раз не пишем: %q", a)
	}
}

func TestRecoveryAlertOnlyAfterDownAlert(t *testing.T) {
	c := &clock{t: t0}
	m := New(c.now, 6*time.Hour)
	m.Failure("Gmail")
	c.t = t0.Add(time.Hour)
	m.Success("Gmail")
	if a := m.Alerts(); len(a) != 0 {
		t.Fatalf("о коротком сбое владелец не слышал — и о восстановлении не нужно: %q", a)
	}

	m.Failure("Gmail")
	c.t = t0.Add(8 * time.Hour)
	if a := m.Alerts(); len(a) != 1 {
		t.Fatalf("ожидал сообщение о сбое, получено %q", a)
	}
	m.Success("Gmail")
	a := m.Alerts()
	if len(a) != 1 || !strings.Contains(a[0], "снова") {
		t.Fatalf("ожидал одно сообщение о восстановлении, получено %q", a)
	}
	if a := m.Alerts(); len(a) != 0 {
		t.Fatalf("о восстановлении пишем один раз: %q", a)
	}
}

func TestSuccessResetsFailureStreak(t *testing.T) {
	c := &clock{t: t0}
	m := New(c.now, 6*time.Hour)
	m.Failure("Gmail")
	c.t = t0.Add(time.Hour)
	m.Success("Gmail")
	c.t = t0.Add(5 * time.Hour)
	m.Failure("Gmail")

	c.t = t0.Add(7 * time.Hour) // 7 часов с первого сбоя, но 2 — с начала новой серии
	if a := m.Alerts(); len(a) != 0 {
		t.Fatalf("успех обнуляет серию сбоев, а сообщение пришло: %q", a)
	}
	st := m.Snapshot()["Gmail"]
	if !st.LastSuccess.Equal(t0.Add(time.Hour)) || !st.LastFailure.Equal(t0.Add(5*time.Hour)) {
		t.Fatalf("снимок не отражает последние события: %+v", st)
	}
}

func TestThresholdPerSource(t *testing.T) {
	c := &clock{t: t0}
	m := New(c.now, 6*time.Hour)
	m.Threshold("Claude", 30*time.Minute)
	m.Failure("Claude")
	m.Failure("Gmail")

	c.t = t0.Add(31 * time.Minute)
	a := m.Alerts()
	if len(a) != 1 || !strings.Contains(a[0], "Claude") {
		t.Fatalf("у Claude свой порог 30 минут, у почты — общий 6 часов: %q", a)
	}
}

func TestOK(t *testing.T) {
	c := &clock{t: t0}
	m := New(c.now, 6*time.Hour)
	if m.OK("Gmail") {
		t.Fatal("без единого успешного опроса источник не может считаться рабочим")
	}
	m.Success("Gmail")
	if !m.OK("Gmail") {
		t.Fatal("после успеха источник рабочий")
	}
	c.t = t0.Add(time.Minute)
	m.Failure("Gmail")
	if m.OK("Gmail") {
		t.Fatal("последний опрос не удался — источник не рабочий")
	}
}
