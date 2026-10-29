package meal

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/chamsslash/tg-claude-bridge/internal/state"
	"github.com/chamsslash/tg-claude-bridge/internal/telegram"
)

// База блюд владельца. Готовит он сам, и блюд у него немного: после первой
// записи блюдо попадает в базу, а в следующий раз его можно выбрать кнопкой
// вместо описания. Claude видит названия из базы и узнаёт в них блюдо на
// фото — так одно блюдо не расползается на «гречку с курицей» и «гречку с
// куриной грудкой».

const (
	// maxBase — сколько блюд помнить; лишние — самые редкие и давние.
	maxBase = 60
	// dishButtons — сколько блюд показывать кнопками.
	dishButtons = 6
	// maxButtonText — длина названия на кнопке.
	maxButtonText = 28
)

type entry struct {
	id string
	d  state.Dish
}

// ranked — блюда базы: сначала частые, при равенстве — недавние.
func ranked(base map[string]state.Dish) []entry {
	out := make([]entry, 0, len(base))
	for id, d := range base {
		out = append(out, entry{id, d})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].d, out[j].d
		if a.Uses != b.Uses {
			return a.Uses > b.Uses
		}
		if !a.Last.Equal(b.Last) {
			return a.Last.After(b.Last)
		}
		return out[i].id < out[j].id
	})
	return out
}

// dishMarkup — блюда из базы кнопками и «Отмена».
func dishMarkup(base map[string]state.Dish) telegram.Markup {
	var rows [][]telegram.Button
	var row []telegram.Button
	for i, e := range ranked(base) {
		if i == dishButtons {
			break
		}
		row = append(row, telegram.Button{Text: truncate(e.d.Name, maxButtonText), Data: dishData + e.id})
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if row != nil {
		rows = append(rows, row)
	}
	rows = append(rows, []telegram.Button{{Text: "Отмена", Data: cancelData}})
	return telegram.Inline(rows)
}

// basePrompt — база и выбранное блюдо для Claude. Названия в базе — от
// Claude и владельца, но теги из них всё равно убираем: блок не должен уметь
// закрыть себя.
func basePrompt(base map[string]state.Dish, picked string) string {
	if len(base) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n<база>\n")
	for _, e := range ranked(base) {
		fmt.Fprintf(&b, "%s — %s\n", e.id, noTags(e.d.Name))
	}
	b.WriteString("</база>\n")
	d, ok := base[picked]
	if !ok {
		return b.String()
	}
	parts := make([]string, 0, len(d.Items))
	for _, it := range d.Items {
		parts = append(parts, fmt.Sprintf("%s %.0f г", noTags(it.Name), it.Grams))
	}
	fmt.Fprintf(&b, "\nВладелец выбрал из базы блюдо %s — «%s», оно на фото. В прошлый раз: %s.\n"+
		"Для него верни saved = %s и то же название; состав бери прежний, а вес порции и компонентов "+
		"оцени заново по фото. Остальное на фото, если есть, — отдельными блюдами.\n",
		picked, noTags(d.Name), strings.Join(parts, ", "), picked)
	return b.String()
}

func noTags(s string) string { return strings.NewReplacer("<", "‹", ">", "›").Replace(s) }

// learn заносит записанный приём в базу: блюдо из базы получает новый
// состав, новое добавляется. Возвращает id и названия добавленных.
func (m *Meal) learn(ps []portion) (ids, names []string) {
	now := m.now()
	err := m.st.Update(func(s *state.State) {
		touched := map[string]bool{}
		for _, p := range ps {
			id := p.saved
			if id == "" {
				id = byName(s.Meal.Dishes, p.name)
			}
			d, ok := s.Meal.Dishes[id]
			if !ok {
				id = newKey(4)
				ids, names = append(ids, id), append(names, p.name)
				d = state.Dish{Name: p.name}
			}
			d.Items, d.Uses, d.Last = p.items, d.Uses+1, now
			if dp := dishProduct(p); dp != nil {
				// Найденный раньше продукт Yazio не теряется, если в этот
				// раз поиск не ответил.
				if dp.YazioID == "" && d.Product != nil && d.Product.YazioID != "" {
					dp = d.Product
				}
				d.Product = dp
			}
			s.Meal.Dishes[id] = d
			touched[id] = true
		}
		// Лишние — самые редкие и давние, но не блюда этого приёма: иначе
		// новое блюдо в полной базе вылетало бы сразу.
		rs := ranked(s.Meal.Dishes)
		for i := len(rs) - 1; i >= 0 && len(s.Meal.Dishes) > maxBase; i-- {
			if !touched[rs[i].id] {
				delete(s.Meal.Dishes, rs[i].id)
			}
		}
	})
	if err != nil {
		slog.Warn("не сохранил базу блюд", "err", err)
	}
	return ids, names
}

// forget убирает из базы блюда, добавленные отменённым приёмом, — если их с
// тех пор не выбирали.
func (m *Meal) forget(ids []string) {
	if len(ids) == 0 {
		return
	}
	m.st.Update(func(s *state.State) {
		for _, id := range ids {
			if d, ok := s.Meal.Dishes[id]; ok && d.Uses <= 1 {
				delete(s.Meal.Dishes, id)
			}
		}
	})
}

func byName(base map[string]state.Dish, name string) string {
	for id, d := range base {
		if strings.EqualFold(d.Name, name) {
			return id
		}
	}
	return ""
}
