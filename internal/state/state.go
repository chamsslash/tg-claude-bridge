// Package state хранит память моста между перезапусками в одном JSON-файле:
// offset Telegram, что из Gmail уже ушло в сводку, действия, которые ещё
// можно отменить кнопкой, и личная база блюд.
//
// Объём — килобайты, поэтому база данных ради этого не нужна. Файл пишется
// целиком и атомарно после каждого изменения.
package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type State struct {
	TelegramOffset int           `json:"telegram_offset"`
	Gmail          GmailState    `json:"gmail"`
	Calendar       CalendarState `json:"calendar"`
	Meal           MealState     `json:"meal"`
}

// GmailState — что из Gmail уже ушло в часовую сводку.
type GmailState struct {
	// Since — с какого момента смотреть письма в следующей сводке.
	Since time.Time `json:"since"`
	// Seen — id писем, уже прошедших через сводку (включая отсеянные).
	Seen map[string]time.Time `json:"seen"`
}

// CalendarState — действия с календарём, которые ещё можно отменить или
// подтвердить кнопкой.
type CalendarState struct {
	Actions map[string]CalendarAction `json:"actions"`
}

// CalendarAction — одно действие: key — данные кнопки, Prev — время события
// до переноса.
type CalendarAction struct {
	Kind      string    `json:"kind"` // create, move, delete
	EventID   string    `json:"event_id"`
	Title     string    `json:"title"`
	When      string    `json:"when"`
	PrevStart EventTime `json:"prev_start"`
	PrevEnd   EventTime `json:"prev_end"`
	At        time.Time `json:"at"`
}

// EventTime — время события в формате Google: дата и время или только дата.
type EventTime struct {
	DateTime string `json:"date_time,omitempty"`
	Date     string `json:"date,omitempty"`
}

// MealState — приёмы пищи в Yazio, которые ещё можно отменить кнопкой, и
// база блюд владельца.
type MealState struct {
	Entries map[string]MealEntry `json:"entries"`
	Dishes  map[string]Dish      `json:"dishes"`
}

// MealEntry — один приём пищи: IDs — id записей «быстрого ввода» в Yazio
// (по одной на блюдо), Day — день дневника (ГГГГ-ММ-ДД), по нему
// проверяется удаление. NewDishes — блюда, которые этот приём добавил в
// базу: отмена убирает и их.
type MealEntry struct {
	IDs       []string  `json:"ids"`
	Day       string    `json:"day"`
	Name      string    `json:"name"`
	NewDishes []string  `json:"new_dishes,omitempty"`
	At        time.Time `json:"at"`
}

// Dish — блюдо из базы владельца: состав и КБЖУ последней порции.
type Dish struct {
	Name  string     `json:"name"`
	Items []DishItem `json:"items"`
	Uses  int        `json:"uses"`
	Last  time.Time  `json:"last"`
}

// DishItem — компонент блюда: КБЖУ — на вес Grams, а не на 100 г.
type DishItem struct {
	Name    string  `json:"name"`
	Grams   float64 `json:"grams"`
	Kcal    float64 `json:"kcal"`
	Protein float64 `json:"protein"`
	Fat     float64 `json:"fat"`
	Carb    float64 `json:"carb"`
}

type Store struct {
	path string

	mu sync.Mutex
	st State
}

// Open читает состояние. Отсутствие файла — первый запуск, побитый файл —
// тоже начинаем с нуля: потеряем offset и память о сводке (письма придут
// повторно), но не встанем колом на старте.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(raw, &s.st); err != nil {
			slog.Warn("файл состояния побит, начинаю с пустого", "path", path, "err", err)
			s.st = State{}
		}
	}
	s.st.normalize()
	return s, nil
}

// Get возвращает копию состояния: менять его можно только через Update.
func (s *Store) Get() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.clone()
}

// Update меняет состояние и сразу сохраняет его на диск. Если запись не
// удалась, в памяти изменение остаётся: следующая удачная запись его догонит.
func (s *Store) Update(fn func(*State)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.st)
	s.st.normalize()
	return s.save()
}

// save пишет файл атомарно: обрыв посреди записи оставил бы усечённый файл,
// и при следующем старте вся память пропала бы.
func (s *Store) save() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // если переименование не дошло — мусор не остаётся

	if err := json.NewEncoder(tmp).Encode(s.st); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func (st *State) normalize() {
	if st.Gmail.Seen == nil {
		st.Gmail.Seen = map[string]time.Time{}
	}
	if st.Calendar.Actions == nil {
		st.Calendar.Actions = map[string]CalendarAction{}
	}
	if st.Meal.Entries == nil {
		st.Meal.Entries = map[string]MealEntry{}
	}
	if st.Meal.Dishes == nil {
		st.Meal.Dishes = map[string]Dish{}
	}
}

func (st State) clone() State {
	st.Gmail.Seen = maps.Clone(st.Gmail.Seen)
	st.Calendar.Actions = maps.Clone(st.Calendar.Actions)
	st.Meal.Entries = maps.Clone(st.Meal.Entries)
	st.Meal.Dishes = maps.Clone(st.Meal.Dishes)
	return st
}
