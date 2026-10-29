// Package health следит за источниками и решает, когда сказать владельцу,
// что источник лёг или поднялся.
//
// Без этого «писем не было» и «почта лежит третьи сутки» снаружи выглядят
// одинаково — тишиной.
package health

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/chamsslash/tg-claude-bridge/internal/msk"
)

type Status struct {
	LastSuccess time.Time `json:"last_success"`
	LastFailure time.Time `json:"last_failure"`
	Down        bool      `json:"down"`
}

type source struct {
	Status
	streakStart time.Time // начало текущей серии сбоев; нулевое — серии нет
	recovered   bool      // поднялся после сообщения о сбое, о восстановлении ещё не сказали
}

// Имена источников. Они же — подлежащее в сообщениях владельцу.
const (
	Claude = "Claude"
	Gmail  = "Gmail"
)

type Monitor struct {
	now       func() time.Time
	downAfter time.Duration

	mu         sync.Mutex
	sources    map[string]*source
	thresholds map[string]time.Duration
}

// New: downAfter — сколько источник должен пролежать, чтобы об этом стоило
// написать. Короткие сбои апстрима — обычное дело, и будить ими владельца
// значило бы приучить его не читать такие сообщения.
func New(now func() time.Time, downAfter time.Duration) *Monitor {
	return &Monitor{now: now, downAfter: downAfter, sources: map[string]*source{}, thresholds: map[string]time.Duration{}}
}

// Threshold задаёт источнику свой порог «лежит». Claude — короче, чем
// Gmail: пока он не отвечает, не работают ни сводки, ни команды.
func (m *Monitor) Threshold(name string, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.thresholds[name] = d
}

// OK — последний опрос источника удался.
func (m *Monitor) OK(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sources[name]
	return ok && !s.LastSuccess.IsZero() && !s.LastFailure.After(s.LastSuccess)
}

func (m *Monitor) get(name string) *source {
	s, ok := m.sources[name]
	if !ok {
		s = &source{}
		m.sources[name] = s
	}
	return s
}

func (m *Monitor) Success(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.get(name)
	s.LastSuccess = m.now()
	s.streakStart = time.Time{}
	if s.Down {
		s.Down = false
		s.recovered = true
	}
}

func (m *Monitor) Failure(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.get(name)
	s.LastFailure = m.now()
	if s.streakStart.IsZero() {
		s.streakStart = s.LastFailure
	}
}

func (m *Monitor) Snapshot() map[string]Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]Status, len(m.sources))
	for name, s := range m.sources {
		out[name] = s.Status
	}
	return out
}

// Alerts возвращает сообщения, которые пора отправить, и считает их
// отправленными: о каждом сбое и каждом восстановлении — ровно один раз.
func (m *Monitor) Alerts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()

	names := make([]string, 0, len(m.sources))
	for name := range m.sources {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []string
	for _, name := range names {
		s := m.sources[name]
		switch {
		case s.recovered:
			s.recovered = false
			out = append(out, fmt.Sprintf("✅ %s: снова на связи", name))
		case !s.Down && !s.streakStart.IsZero() && now.Sub(s.streakStart) > m.threshold(name):
			s.Down = true
			out = append(out, fmt.Sprintf("⚠️ %s: нет связи с %s", name, s.streakStart.In(msk.Location).Format("15:04 02.01")))
		}
	}
	return out
}

func (m *Monitor) threshold(name string) time.Duration {
	if d, ok := m.thresholds[name]; ok {
		return d
	}
	return m.downAfter
}
