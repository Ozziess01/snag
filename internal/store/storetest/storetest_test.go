// Интеграционные тесты хранилищ на настоящих Postgres и ClickHouse.
// Запускаются, если заданы SNAG_TEST_PG и SNAG_TEST_CH (например, базы
// из docker-compose.yml), иначе пропускаются.
package storetest

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Ozziess01/snag/internal/ingest"
	"github.com/Ozziess01/snag/internal/store"
	"github.com/Ozziess01/snag/internal/store/ch"
	"github.com/Ozziess01/snag/internal/store/pg"
)

func stores(t *testing.T) (*pg.Store, *ch.Store) {
	t.Helper()
	pgDSN, chDSN := os.Getenv("SNAG_TEST_PG"), os.Getenv("SNAG_TEST_CH")
	if pgDSN == "" || chDSN == "" {
		t.Skip("SNAG_TEST_PG / SNAG_TEST_CH не заданы")
	}
	ctx := context.Background()
	p, err := pg.Open(ctx, pgDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	c, err := ch.Open(ctx, chDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	// Дважды: миграции должны спокойно переживать повторный запуск.
	for range 2 {
		if err := p.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if err := c.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return p, c
}

func TestProjectsAndKeys(t *testing.T) {
	p, _ := stores(t)
	ctx := context.Background()

	a, err := p.CreateProject(ctx, "Интернет-магазин")
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.CreateProject(ctx, "Интернет-магазин")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || a.Slug == b.Slug || len(a.PublicKey) != 32 {
		t.Fatalf("проекты с одинаковым именем: %+v %+v", a, b)
	}
	if !strings.HasPrefix(a.Slug, "internet-magazin") {
		t.Errorf("slug %q: ждали транслитерацию", a.Slug)
	}

	got, err := p.ByKey(ctx, a.PublicKey)
	if err != nil || got.ID != a.ID {
		t.Fatalf("ByKey: %+v %v", got, err)
	}
	if _, err := p.ByKey(ctx, "0000000000000000000000000000dead"); !errors.Is(err, ingest.ErrUnknownKey) {
		t.Fatalf("чужой ключ: %v", err)
	}
}

func TestIssueLifecycle(t *testing.T) {
	p, _ := stores(t)
	ctx := context.Background()
	proj, err := p.CreateProject(ctx, "lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	delta := func(fp uint64, ts time.Time, n int64) store.IssueDelta {
		return store.IssueDelta{ProjectID: proj.ID, Fingerprint: fp, Kind: "stacktrace", Title: "TypeError", Level: "error",
			Platform: "javascript", FirstSeen: ts, LastSeen: ts, Count: n}
	}
	// Отпечаток с установленным старшим битом: проверяем, что uint64 → bigint
	// и обратно не теряет значение.
	const fp = uint64(0xfedcba9876543210)

	res, err := p.UpsertIssues(ctx, []store.IssueDelta{delta(fp, t0, 3), delta(7, t0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || !res[0].Created || !res[1].Created {
		t.Fatalf("первая встреча — новые проблемы: %+v", res)
	}
	var id uint64
	for _, r := range res {
		if r.Fingerprint == fp {
			id = r.ID
		}
	}
	if id == 0 {
		t.Fatalf("отпечаток %x потерялся: %+v", fp, res)
	}

	res, _ = p.UpsertIssues(ctx, []store.IssueDelta{delta(fp, t0.Add(time.Minute), 2)})
	if res[0].Created || res[0].Regressed || res[0].ID != id {
		t.Fatalf("повтор — не новая и не регрессия: %+v", res)
	}

	if err := p.SetIssueStatus(ctx, id, "resolved"); err != nil {
		t.Fatal(err)
	}
	// Запоздавшее событие из прошлого не переоткрывает решённую проблему.
	res, _ = p.UpsertIssues(ctx, []store.IssueDelta{delta(fp, t0.Add(2*time.Minute), 1)})
	if res[0].Regressed {
		t.Fatalf("старое событие не должно быть регрессией: %+v", res)
	}
	// А свежее — переоткрывает.
	res, _ = p.UpsertIssues(ctx, []store.IssueDelta{delta(fp, time.Now().Add(time.Minute).UTC(), 1)})
	if !res[0].Regressed {
		t.Fatalf("свежее событие после решения — регрессия: %+v", res)
	}
	res, _ = p.UpsertIssues(ctx, []store.IssueDelta{delta(fp, time.Now().Add(2*time.Minute).UTC(), 1)})
	if res[0].Regressed {
		t.Fatalf("регрессия отмечается один раз: %+v", res)
	}
}

func TestEventsAndStats(t *testing.T) {
	p, c := stores(t)
	ctx := context.Background()
	proj, err := p.CreateProject(ctx, "events")
	if err != nil {
		t.Fatal(err)
	}
	// Миллисекунды в метках времени нарочно: на них ломалось сравнение.
	hour := time.Now().UTC().Truncate(time.Hour).Add(123 * time.Millisecond)
	ev := func(issue uint64, id string, ts time.Time, user, browser string) store.Event {
		return store.Event{ProjectID: proj.ID, IssueID: issue, EventID: id, Timestamp: ts, ReceivedAt: ts,
			Level: "error", Platform: "python", Title: "E", UserKey: user,
			Tags: map[string]string{"browser": browser}, Data: `{"message":"` + id + `"}`}
	}
	rows := []store.Event{
		ev(1, "00000000000000000000000000000001", hour.Add(-2*time.Hour), "id:1", "Chrome 128"),
		ev(1, "00000000000000000000000000000002", hour.Add(-2*time.Hour+time.Minute), "id:1", "Chrome 128"),
		ev(1, "00000000000000000000000000000003", hour.Add(5*time.Minute), "id:2", "Firefox 130"),
		ev(1, "00000000000000000000000000000004", hour.Add(6*time.Minute), "", "Chrome 128"),
		ev(2, "00000000000000000000000000000005", hour.Add(time.Minute), "", "Safari 17"),
	}
	if err := c.InsertEvents(ctx, rows); err != nil {
		t.Fatal(err)
	}

	h0 := hour.Truncate(time.Hour)
	period := store.Period{Since: h0.Add(-23 * time.Hour), Until: h0.Add(time.Hour), Step: time.Hour}
	stats, err := c.IssueStats(ctx, proj.ID, 0, period)
	if err != nil {
		t.Fatal(err)
	}
	s1 := stats[1]
	if s1.Events != 4 || s1.Users != 2 {
		t.Errorf("проблема 1: %+v, ждали 4 события и 2 пользователя (пустой user_key не считается)", s1)
	}
	if len(s1.Buckets) != 24 || s1.Buckets[21] != 2 || s1.Buckets[23] != 2 {
		t.Errorf("интервалы проблемы 1: %v", s1.Buckets)
	}
	if stats[2].Events != 1 || stats[2].Users != 0 {
		t.Errorf("проблема 2: %+v", stats[2])
	}
	only, _ := c.IssueStats(ctx, proj.ID, 2, period)
	if len(only) != 1 || only[2].Events != 1 {
		t.Errorf("фильтр по проблеме: %+v", only)
	}

	tags, totals, err := c.IssueTags(ctx, proj.ID, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if b := tags["browser"]; len(b) != 2 || b[0].Value != "Chrome 128" || b[0].Count != 3 || totals["browser"] != 4 {
		t.Errorf("теги: %+v %v", tags, totals)
	}

	list, err := c.IssueEvents(ctx, proj.ID, 1, time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 || list[0].EventID != "00000000000000000000000000000004" {
		t.Fatalf("события от новых к старым: %+v", list)
	}

	latest, err := c.Event(ctx, proj.ID, 1, "latest")
	if err != nil || latest.EventID != "00000000000000000000000000000004" || latest.Data == "" {
		t.Fatalf("latest: %+v %v", latest, err)
	}
	mid, err := c.Event(ctx, proj.ID, 1, "00000000000000000000000000000003")
	if err != nil {
		t.Fatal(err)
	}
	older, newer, err := c.Neighbors(ctx, proj.ID, 1, mid)
	if err != nil || older != "00000000000000000000000000000002" || newer != "00000000000000000000000000000004" {
		t.Fatalf("соседи: %q %q %v", older, newer, err)
	}
	if _, newest, _ := c.Neighbors(ctx, proj.ID, 1, latest); newest != "" {
		t.Fatalf("у самого нового события нет «новее»: %q", newest)
	}
	single, err := c.Event(ctx, proj.ID, 2, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if o, n, _ := c.Neighbors(ctx, proj.ID, 2, single); o != "" || n != "" {
		t.Fatalf("у единственного события нет соседей: %q %q", o, n)
	}
	page, _ := c.IssueEvents(ctx, proj.ID, 1, list[1].Timestamp, 10)
	if len(page) != 2 || page[0].EventID != "00000000000000000000000000000002" {
		t.Fatalf("следующая страница событий: %+v", page)
	}
	if _, err := c.Event(ctx, proj.ID, 1, "ffffffffffffffffffffffffffffffff"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("чужое событие: %v", err)
	}
}

func TestChannels(t *testing.T) {
	p, _ := stores(t)
	ctx := context.Background()
	proj, err := p.CreateProject(ctx, "channels")
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.SaveChannel(ctx, store.Channel{ProjectID: proj.ID, Target: "-100200", OnNew: true, SpikeThreshold: 10})
	if err != nil || c.ID == 0 || c.SpikeWindow != 5 {
		t.Fatalf("создание: %+v %v", c, err)
	}
	if _, err := p.SaveChannel(ctx, store.Channel{ProjectID: proj.ID, Target: "-100200"}); !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("дубль: %v", err)
	}
	var bad store.ValidationError
	if _, err := p.SaveChannel(ctx, store.Channel{ProjectID: proj.ID, Target: "не чат"}); !errors.As(err, &bad) {
		t.Fatalf("проверка: %v", err)
	}
	c.OnRegression, c.SpikeWindow = true, 15
	if _, err := p.SaveChannel(ctx, c); err != nil {
		t.Fatal(err)
	}
	list, _ := p.Channels(ctx, proj.ID)
	if len(list) != 1 || !list[0].OnRegression || list[0].SpikeWindow != 15 {
		t.Fatalf("после изменения: %+v", list)
	}
	other, _ := p.CreateProject(ctx, "other")
	if err := p.DeleteChannel(ctx, other.ID, c.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("канал чужого проекта не удаляется: %v", err)
	}
	if err := p.DeleteChannel(ctx, proj.ID, c.ID); err != nil {
		t.Fatal(err)
	}
}

func TestUsersAndSessions(t *testing.T) {
	p, _ := stores(t)
	ctx := context.Background()
	email := "Admin+" + time.Now().Format("150405.000") + "@Example.com"
	if _, err := p.CreateUser(ctx, email, "short"); !errors.Is(err, store.ErrWeakPassword) {
		t.Fatalf("короткий пароль: %v", err)
	}
	u, err := p.CreateUser(ctx, email, "correct horse battery")
	if err != nil || u.Email != strings.ToLower(email) {
		t.Fatalf("CreateUser: %+v %v", u, err)
	}
	if _, _, err := p.Login(ctx, email, "wrong password"); !errors.Is(err, store.ErrBadCredentials) {
		t.Fatalf("неверный пароль: %v", err)
	}
	if _, _, err := p.Login(ctx, "nobody@example.com", "whatever1"); !errors.Is(err, store.ErrBadCredentials) {
		t.Fatalf("нет пользователя: %v", err)
	}
	token, got, err := p.Login(ctx, "  "+strings.ToUpper(email), "correct horse battery")
	if err != nil || got.ID != u.ID {
		t.Fatalf("Login: %+v %v", got, err)
	}
	if me, err := p.UserBySession(ctx, token); err != nil || me.ID != u.ID {
		t.Fatalf("сессия: %+v %v", me, err)
	}
	if err := p.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UserBySession(ctx, token); !errors.Is(err, store.ErrNoSession) {
		t.Fatalf("после выхода: %v", err)
	}
}
