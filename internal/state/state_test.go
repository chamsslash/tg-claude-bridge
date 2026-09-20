package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileStartsFromZero(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "нет-такого", "offset.json"))
	got, err := s.Load()
	if err != nil {
		t.Fatalf("отсутствие файла — не ошибка, а первый запуск: %v", err)
	}
	if got != 0 {
		t.Errorf("ожидал 0, получил %d", got)
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	// Каталог намеренно ещё не создан: служба должна поднимать его сама.
	s := New(filepath.Join(t.TempDir(), "var", "offset.json"))
	if err := s.Save(4242); err != nil {
		t.Fatalf("не сохранил: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("не прочитал: %v", err)
	}
	if got != 4242 {
		t.Errorf("ожидал 4242, получил %d", got)
	}
}

func TestLoadBrokenFileDoesNotWedge(t *testing.T) {
	// Побитый файл не должен мешать старту: потеряем позицию, но не встанем.
	dir := t.TempDir()
	path := filepath.Join(dir, "offset.json")
	os.WriteFile(path, []byte("{обрублено"), 0o644)

	got, err := New(path).Load()
	if err != nil {
		t.Fatalf("побитый файл не должен ронять старт: %v", err)
	}
	if got != 0 {
		t.Errorf("ожидал 0, получил %d", got)
	}
}

func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "offset.json"))
	for i := range 5 {
		if err := s.Save(i); err != nil {
			t.Fatal(err)
		}
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("после записи должен остаться один файл, есть: %v", names)
	}
}
