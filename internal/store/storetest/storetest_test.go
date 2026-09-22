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
	delta := func(fp uint64, ts time.Time, n int64) pg.IssueDelta {
		return pg.IssueDelta{ProjectID: proj.ID, Fingerprint: fp, Kind: "stacktrace", Title: "TypeError", Level: "error",
			Platform: "javascript", FirstSeen: ts, LastSeen: ts, Count: n}
	}
	// Отпечаток с установленным старшим битом: проверяем, что uint64 → bigint
	// и обратно не теряет значение.
	const fp = uint64(0xfedcba9876543210)

	res, err := p.UpsertIssues(ctx, []pg.IssueDelta{delta(fp, t0, 3), delta(7, t0, 1)})
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

	res, _ = p.UpsertIssues(ctx, []pg.IssueDelta{delta(fp, t0.Add(time.Minute), 2)})
	if res[0].Created || res[0].Regressed || res[0].ID != id {
		t.Fatalf("повтор — не новая и не регрессия: %+v", res)
	}

	if err := p.SetIssueStatus(ctx, id, "resolved"); err != nil {
		t.Fatal(err)
	}
	// Запоздавшее событие из прошлого не переоткрывает решённую проблему.
	res, _ = p.UpsertIssues(ctx, []pg.IssueDelta{delta(fp, t0.Add(2*time.Minute), 1)})
	if res[0].Regressed {
		t.Fatalf("старое событие не должно быть регрессией: %+v", res)
	}
	// А свежее — переоткрывает.
	res, _ = p.UpsertIssues(ctx, []pg.IssueDelta{delta(fp, time.Now().Add(time.Minute).UTC(), 1)})
	if !res[0].Regressed {
		t.Fatalf("свежее событие после решения — регрессия: %+v", res)
	}
	res, _ = p.UpsertIssues(ctx, []pg.IssueDelta{delta(fp, time.Now().Add(2*time.Minute).UTC(), 1)})
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
	now := time.Now().UTC()
	ev := func(issue uint64, user string) ch.Event {
		return ch.Event{ProjectID: proj.ID, IssueID: issue, EventID: "9ec79c33ec9942ab8353589fcb2e04dc", Timestamp: now,
			ReceivedAt: now, Level: "error", Platform: "python", Title: "E", UserKey: user,
			Tags: map[string]string{"browser": "Chrome"}, Data: `{"a":1}`}
	}
	rows := []ch.Event{ev(1, "id:1"), ev(1, "id:1"), ev(1, "id:2"), ev(1, ""), ev(2, "")}
	if err := c.InsertEvents(ctx, rows); err != nil {
		t.Fatal(err)
	}
	stats, err := c.IssueStats(ctx, proj.ID, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if s := stats[1]; s.Events != 4 || s.Users != 2 {
		t.Errorf("проблема 1: %+v, ждали 4 события и 2 пользователя (пустой user_key не считается)", s)
	}
	if s := stats[2]; s.Events != 1 || s.Users != 0 {
		t.Errorf("проблема 2: %+v", s)
	}
}
