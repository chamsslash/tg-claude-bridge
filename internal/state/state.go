// Package state хранит offset Telegram между перезапусками моста.
package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

type Store struct {
	path string
}

func New(path string) *Store {
	return &Store{path: path}
}

// Load читает сохранённый offset. Отсутствие файла — не ошибка: это первый
// запуск, начинаем с нуля и получаем всё, что Telegram ещё держит у себя.
func (s *Store) Load() (int, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	var v struct {
		Offset int `json:"offset"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		// Побитый файл лечится сам: потеряем позицию, но не встанем колом.
		return 0, nil
	}
	return v.Offset, nil
}

// Save записывает offset атомарно: обрыв посреди записи оставил бы усечённый
// файл, и мост при следующем старте переиграл бы всю очередь команд заново.
func (s *Store) Save(offset int) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".offset-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // если переименование не дошло — мусор не остаётся

	if err := json.NewEncoder(tmp).Encode(map[string]int{"offset": offset}); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
