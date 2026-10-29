package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenMissingFile(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := s.Get()
	if got.TelegramOffset != 0 || !got.Gmail.Since.IsZero() {
		t.Fatalf("первый запуск должен начинаться с пустого состояния, получено %+v", got)
	}
	if got.Gmail.Seen == nil || got.Calendar.Actions == nil || got.Meal.Entries == nil {
		t.Fatal("карты не должны быть nil: иначе первая же запись в них паникует")
	}
}

func TestUpdatePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	if err := s.Update(func(st *State) {
		st.TelegramOffset = 7
		st.Meal.Entries["a"] = MealEntry{ID: "x", At: at}
	}); err != nil {
		t.Fatal(err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := again.Get()
	if got.TelegramOffset != 7 || !got.Meal.Entries["a"].At.Equal(at) {
		t.Fatalf("состояние не пережило перезапуск: %+v", got)
	}
}

func TestOpenCorruptFileStartsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("побитый файл должен лечиться пустым состоянием, а не ронять старт: %v", err)
	}
	if s.Get().TelegramOffset != 0 {
		t.Fatal("ожидал пустое состояние")
	}
}

func TestGetIsCopy(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	g := s.Get()
	g.Gmail.Seen["x"] = time.Now()
	if _, ok := s.Get().Gmail.Seen["x"]; ok {
		t.Fatal("Get обязан отдавать копию: правка снаружи не должна менять состояние мимо Update")
	}
}
