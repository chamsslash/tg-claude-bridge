package yazio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chamsslash/tg-claude-bridge/internal/msk"
)

// fakeYazio — подставной API: выдаёт токены по порядку и записывает запросы.
type fakeYazio struct {
	mu       sync.Mutex
	logins   int
	reqs     []string
	bodies   []string
	expired  map[string]bool // токены, на которые отвечать 401
	blocked  bool
	badLogin bool
	day      string
	get      map[string]string // ответы на GET по пути; остальные — day
}

func (f *fakeYazio) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		raw, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/oauth/token" {
			var b map[string]string
			json.Unmarshal(raw, &b)
			if b["grant_type"] != "password" || b["username"] != "u@x.ru" || b["password"] != "pw" || b["client_id"] != clientID {
				t.Errorf("вход: %v", b)
			}
			if f.badLogin {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
			f.logins++
			io.WriteString(w, `{"access_token":"t`+string(rune('0'+f.logins))+`","expires_in":172800,"token_type":"bearer"}`)
			return
		}
		if r.Header.Get("User-Agent") != "UA/1" || r.Header.Get("Accept-Language") != "ru-RU" {
			t.Errorf("заголовки %v", r.Header)
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.reqs = append(f.reqs, r.Method+" "+r.URL.RequestURI()+" "+tok)
		f.bodies = append(f.bodies, string(raw))
		switch {
		case f.expired[tok]:
			w.WriteHeader(http.StatusUnauthorized)
		case f.blocked:
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"error":"version_blocked"}`)
		case r.Method == http.MethodGet && f.get[r.URL.Path] != "":
			io.WriteString(w, f.get[r.URL.Path])
		case r.Method == http.MethodGet:
			io.WriteString(w, f.day)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

func newClient(t *testing.T, f *fakeYazio) *Client {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	c := New("u@x.ru", "pw", "UA/1", time.Second)
	c.base = srv.URL
	return c
}

func TestAddBody(t *testing.T) {
	f := &fakeYazio{}
	c := newClient(t, f)
	at := time.Date(2026, 10, 28, 13, 5, 9, 0, msk.Location)
	es := []Entry{
		{ID: "id-1", Time: at, Daytime: Lunch, Name: "Гречка с курицей", Kcal: 620, Protein: 38, Fat: 18.5, Carb: 72},
		{ID: "id-2", Time: at, Daytime: Lunch, Name: "Компот", Kcal: 90, Protein: 0, Fat: 0, Carb: 22},
		{ID: "id-3", Time: at, Daytime: Lunch, Name: "Шорли", Kcal: 150, ProductID: "prod-1", Amount: 500},
	}
	if err := c.Add(context.Background(), es); err != nil {
		t.Fatal(err)
	}
	if f.reqs[0] != "POST /user/consumed-items t1" {
		t.Fatalf("запрос %q", f.reqs[0])
	}
	var got map[string]any
	json.Unmarshal([]byte(f.bodies[0]), &got)
	want := map[string]any{
		"products": []any{map[string]any{
			"id": "id-3", "product_id": "prod-1", "amount": 500.0, "serving": nil, "serving_quantity": nil,
			"daytime": "lunch", "date": "2026-10-28 13:05:09",
		}},
		"recipe_portions": []any{},
		"simple_products": []any{map[string]any{
			"id": "id-1", "date": "2026-10-28 13:05:09", "daytime": "lunch", "name": "Гречка с курицей",
			"nutrients":       map[string]any{"energy.energy": 620.0, "nutrient.protein": 38.0, "nutrient.fat": 18.5, "nutrient.carb": 72.0},
			"is_ai_generated": false,
		}, map[string]any{
			"id": "id-2", "date": "2026-10-28 13:05:09", "daytime": "lunch", "name": "Компот",
			"nutrients":       map[string]any{"energy.energy": 90.0, "nutrient.protein": 0.0, "nutrient.fat": 0.0, "nutrient.carb": 22.0},
			"is_ai_generated": false,
		}},
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("тело\n%s\nwant\n%s", gotJSON, wantJSON)
	}
}

// Удаляют строкой, а не массивом: массив API не принимает.
func TestDeleteBody(t *testing.T) {
	f := &fakeYazio{}
	c := newClient(t, f)
	ctx := context.Background()
	if err := c.Delete(ctx, "id-1", false); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, "id-2", true); err != nil {
		t.Fatal(err)
	}
	if f.reqs[0] != "DELETE /user/consumed-items t1" || f.bodies[0] != `{"simple_products":"id-1"}` ||
		f.bodies[1] != `{"products":"id-2"}` {
		t.Fatalf("%q %q", f.reqs, f.bodies)
	}
}

func TestDay(t *testing.T) {
	f := &fakeYazio{day: `{"products":[{"id":"p"}],"recipe_portions":[],"simple_products":[{"id":"a","name":"x"},{"id":"b"}]}`}
	c := newClient(t, f)
	ids, err := c.Day(context.Background(), "2026-10-28")
	if err != nil || strings.Join(ids, ",") != "p,a,b" {
		t.Fatalf("%v %v", ids, err)
	}
	if f.reqs[0] != "GET /user/consumed-items?date=2026-10-28 t1" {
		t.Fatalf("%q", f.reqs[0])
	}
}

// Ответ записан с живого API 29.10.2026 (числа изменены).
const dailySummary = `{"activity_energy":150,"consume_activity_energy":true,"steps":0,"water_intake":0,
"goals":{"energy.energy":2036.55,"water":2000,"activity.step":10000,"nutrient.protein":99.3439,"nutrient.fat":65.6952,"nutrient.carb":248.3598,"bodyvalue.weight":64},
"meals":{"breakfast":{"energy_goal":610.9,"nutrients":{"energy.energy":0,"nutrient.carb":0,"nutrient.fat":0,"nutrient.protein":0}},
"lunch":{"energy_goal":814.6,"nutrients":{"energy.energy":120,"nutrient.carb":10,"nutrient.fat":6,"nutrient.protein":6}},
"dinner":{"energy_goal":509.1,"nutrients":{"energy.energy":895,"nutrient.carb":63.13,"nutrient.fat":51.61,"nutrient.protein":46.65}},
"snack":{"energy_goal":101.8,"nutrients":{"energy.energy":0,"nutrient.carb":0,"nutrient.fat":0,"nutrient.protein":0}}}}`

func TestSummary(t *testing.T) {
	f := &fakeYazio{day: dailySummary}
	c := newClient(t, f)
	s, err := c.Summary(context.Background(), "2026-10-29")
	if err != nil {
		t.Fatal(err)
	}
	if f.reqs[0] != "GET /user/widgets/daily-summary?date=2026-10-29 t1" {
		t.Fatalf("%q", f.reqs[0])
	}
	if s.Kcal != 1015 || s.KcalGoal != 2186.55 || s.Protein != 52.65 || s.ProteinGoal != 99.3439 ||
		s.Fat != 57.61 || s.Carb != 73.13 || s.CarbGoal != 248.3598 {
		t.Fatalf("%+v", s)
	}
	f.day = strings.Replace(dailySummary, `"consume_activity_energy":true`, `"consume_activity_energy":false`, 1)
	if s, _ := c.Summary(context.Background(), "2026-10-29"); s.KcalGoal != 2036.55 {
		t.Fatalf("без учёта активности цель %v", s.KcalGoal)
	}
	f.day = `{"goals":{},"meals":{}}`
	if _, err := c.Summary(context.Background(), "2026-10-29"); err == nil {
		t.Fatal("без цели остаток не посчитать")
	}
}

// Ответ поиска записан с живого API 29.10.2026 (сокращён).
const searchResult = `[{"score":110,"name":"Шорли яблочный с клубникой и мятой","product_id":"e129","serving":"bottle","serving_quantity":1,"amount":500,"base_unit":"ml","producer":"ВкусВилл","is_verified":false,"nutrients":{"energy.energy":0.3,"nutrient.carb":0.075,"nutrient.fat":0,"nutrient.protein":0},"countries":["RU"],"language":"ru"}]`

// Поиск берёт пол и страну базы из профиля, профиль читается один раз.
func TestSearch(t *testing.T) {
	f := &fakeYazio{get: map[string]string{
		"/user":            `{"sex":"female","country":"DE","food_database_country":"RU","email":"u@x.ru"}`,
		"/products/search": searchResult,
	}}
	c := newClient(t, f)
	ctx := context.Background()
	ps, err := c.Search(ctx, "шорли клубника")
	if err != nil {
		t.Fatal(err)
	}
	want := Product{ID: "e129", Name: "Шорли яблочный с клубникой и мятой", Producer: "ВкусВилл", Unit: "ml", Kcal: 0.3, Carb: 0.075}
	if len(ps) != 1 || ps[0] != want {
		t.Fatalf("%+v", ps)
	}
	c.Search(ctx, "кефир")
	wantReqs := []string{
		"GET /user t1",
		"GET /products/search?countries=RU&query=%D1%88%D0%BE%D1%80%D0%BB%D0%B8+%D0%BA%D0%BB%D1%83%D0%B1%D0%BD%D0%B8%D0%BA%D0%B0&sex=female t1",
		"GET /products/search?countries=RU&query=%D0%BA%D0%B5%D1%84%D0%B8%D1%80&sex=female t1",
	}
	if strings.Join(f.reqs, "\n") != strings.Join(wantReqs, "\n") {
		t.Fatalf("запросы\n%s", strings.Join(f.reqs, "\n"))
	}
}

func TestSearchNeedsProfile(t *testing.T) {
	f := &fakeYazio{get: map[string]string{"/user": `{"email":"u@x.ru"}`}}
	if _, err := newClient(t, f).Search(context.Background(), "кефир"); err == nil {
		t.Fatal("без пола поиск выдаст не то")
	}
}

// Токен входа переиспользуется; протух — один повторный вход.
func TestTokenReuseAndRelogin(t *testing.T) {
	f := &fakeYazio{expired: map[string]bool{}}
	c := newClient(t, f)
	ctx := context.Background()
	c.Delete(ctx, "a", false)
	c.Delete(ctx, "b", false)
	if f.logins != 1 {
		t.Fatalf("входов %d, want 1", f.logins)
	}
	f.expired["t1"] = true
	if err := c.Delete(ctx, "c", false); err != nil {
		t.Fatal(err)
	}
	if f.logins != 2 || !strings.HasSuffix(f.reqs[len(f.reqs)-1], " t2") {
		t.Fatalf("входов %d, запросы %v", f.logins, f.reqs)
	}
}

// 401 и после повторного входа — ошибка, а не бесконечный цикл.
func TestReloginOnce(t *testing.T) {
	f := &fakeYazio{expired: map[string]bool{"t1": true, "t2": true, "t3": true}}
	c := newClient(t, f)
	if err := c.Delete(context.Background(), "a", false); err == nil {
		t.Fatal("ожидал ошибку")
	}
	if f.logins != 2 {
		t.Fatalf("входов %d, want 2", f.logins)
	}
}

func TestErrors(t *testing.T) {
	f := &fakeYazio{badLogin: true}
	if err := newClient(t, f).Delete(context.Background(), "a", false); !errors.Is(err, ErrAuth) {
		t.Fatalf("плохой пароль: %v", err)
	}
	f = &fakeYazio{blocked: true}
	if err := newClient(t, f).Delete(context.Background(), "a", false); !errors.Is(err, ErrVersionBlocked) {
		t.Fatalf("версия: %v", err)
	}
}

// Пароль не уходит в текст ошибок: они попадают в логи.
func TestErrorsDoNotLeakPassword(t *testing.T) {
	c := New("u@x.ru", "SECRET-PW", "", time.Second)
	c.base = "http://127.0.0.1:1"
	err := c.Delete(context.Background(), "a", false)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err %v", err)
	}
}

func TestNewID(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	a, b := NewID(), NewID()
	if !re.MatchString(a) || a == b {
		t.Fatalf("%q %q", a, b)
	}
}

func TestEnabled(t *testing.T) {
	if New("", "", "", time.Second).Enabled() || !New("u", "p", "", time.Second).Enabled() {
		t.Fatal("Enabled")
	}
	if New("u", "p", "", time.Second).ua != DefaultUserAgent {
		t.Fatal("пустой User-Agent — версия по умолчанию")
	}
}
