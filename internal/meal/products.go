package meal

import (
	"context"
	"log/slog"
	"math"
	"strings"
	"unicode"

	"github.com/chamsslash/tg-claude-bridge/internal/state"
	"github.com/chamsslash/tg-claude-bridge/internal/yazio"
)

// Продукты из базы Yazio. Покупное в упаковке (шорли, йогурт, батончик)
// Claude помечает брендом и коротким запросом; сервис ищет продукт в базе
// Yazio и, если нашёл тот же — бренд совпал, калории на 100 г близки к
// оценке Claude, — пишет его продуктом из базы, а не «быстрым вводом».
// Записи в базе добавляют пользователи, и ошибки в них бывают: оценка
// Claude (с этикетки или из интернета) служит им проверкой.

const (
	// Калории на 100 г у Yazio и у Claude совпали, если разница не больше
	// kcalShare от оценки Claude или kcalSlack ккал — запас для напитков,
	// где 15% от 30 ккал — меньше погрешности этикетки.
	kcalShare = 0.15
	kcalSlack = 8

	maxQuery = 60
)

// productRef — покупной продукт в ответе Claude.
type productRef struct {
	Brand string `json:"brand"`
	Query string `json:"query"`
}

// match подставляет продукты из базы Yazio в покупные блюда. Блюдо из базы
// блюд, для которого продукт уже находился, берёт его без поиска. Не
// нашлось — блюдо остаётся «быстрым вводом» с оценкой Claude; поиск не
// ответил — тоже, и остальные блюда уже не ищутся.
func (m *Meal) match(ctx context.Context, ps []portion) {
	for i := range ps {
		p := &ps[i]
		if p.product == nil || len(p.items) != 1 || p.items[0].Grams <= 0 {
			continue
		}
		if p.known != nil {
			use(p, *p.known)
			continue
		}
		brand, query := line(p.product.Brand, maxQuery), line(p.product.Query, maxQuery)
		if norm(brand) == "" || query == "" {
			continue
		}
		it := p.items[0]
		prod, ok, err := m.find(ctx, brand, query, it.Kcal/it.Grams)
		if err != nil {
			slog.Warn("не нашёл продукт в базе Yazio", "err", err)
			return
		}
		if ok {
			use(p, prod)
		}
	}
}

// find ищет сначала с брендом — он бывает и в названии, — потом без него:
// поиск Yazio требует в названии все слова запроса. kcal — оценка Claude на
// 1 г.
func (m *Meal) find(ctx context.Context, brand, query string, kcal float64) (yazio.Product, bool, error) {
	for _, q := range []string{brand + " " + query, query} {
		found, err := m.d.Search(ctx, q)
		if err != nil {
			return yazio.Product{}, false, err
		}
		if p, ok := pick(found, brand, kcal); ok {
			return p, true, nil
		}
	}
	return yazio.Product{}, false, nil
}

// pick — первый продукт того же бренда с близкими калориями; kcal — на 1 г.
func pick(found []yazio.Product, brand string, kcal float64) (yazio.Product, bool) {
	b := norm(brand)
	for _, p := range found {
		pb := norm(p.Producer)
		if p.Unit != "g" && p.Unit != "ml" || pb == "" || !strings.Contains(pb, b) && !strings.Contains(b, pb) {
			continue
		}
		if math.Abs(p.Kcal-kcal)*100 <= math.Max(kcalShare*kcal*100, kcalSlack) {
			return p, true
		}
	}
	return yazio.Product{}, false
}

// norm — бренд для сравнения: «ВкусВилл», «вкусвилл» и «Вкус Вилл» равны.
func norm(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == 'ё' || r == 'Ё':
			return 'е'
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}

// use делает блюдо продуктом из базы: вес — оценка Claude, КБЖУ — по базе.
func use(p *portion, prod yazio.Product) {
	g := p.items[0].Grams
	name := line(prod.Name, maxItemName)
	if name == "" {
		name = p.items[0].Name
	}
	it := item{Name: name, Grams: g, Kcal: round1(prod.Kcal * g), Protein: round1(prod.Protein * g),
		Fat: round1(prod.Fat * g), Carb: round1(prod.Carb * g)}
	p.items = []item{it}
	p.entry.Kcal, p.entry.Protein, p.entry.Fat, p.entry.Carb = it.Kcal, it.Protein, it.Fat, it.Carb
	p.entry.ProductID, p.entry.Amount = prod.ID, g
	p.found = &prod
}

// dishProduct — что запомнить в базе блюд о покупном блюде; nil — блюдо не
// покупное.
func dishProduct(p portion) *state.DishProduct {
	if p.product == nil {
		return nil
	}
	dp := &state.DishProduct{Brand: line(p.product.Brand, maxQuery), Query: line(p.product.Query, maxQuery)}
	if f := p.found; f != nil {
		dp.YazioID, dp.Name, dp.Unit = f.ID, f.Name, f.Unit
		dp.Kcal, dp.Protein, dp.Fat, dp.Carb = f.Kcal, f.Protein, f.Fat, f.Carb
	}
	return dp
}
