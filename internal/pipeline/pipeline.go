// Package pipeline — путь события от приёма до баз.
//
//	ingest ──► Queue ──► Worker: группировка ──► Postgres (проблемы)
//	                                         └─► ClickHouse (события)
//
// Приём кладёт события в очередь и сразу отвечает SDK. Воркер копит пачку
// (до BatchSize событий или FlushEvery времени) и пишет её в базы разом:
// один запрос в Postgres и одна вставка в ClickHouse на пачку, а не на
// каждое событие.
package pipeline

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Ozziess01/snag/internal/event"
	"github.com/Ozziess01/snag/internal/grouping"
	"github.com/Ozziess01/snag/internal/ingest"
	"github.com/Ozziess01/snag/internal/store/ch"
	"github.com/Ozziess01/snag/internal/store/pg"
)

// ---------- очередь ----------

// Queue — очередь запросов (каждый — пачка событий одного конверта).
// Живёт в памяти: при аварийном падении процесса недописанное теряется,
// при штатной остановке воркер дописывает всё. Место для постоянной
// очереди (Redis Streams, NATS) — за этим же интерфейсом ingest.Sink.
type Queue struct {
	ch     chan []ingest.Accepted
	closed atomic.Bool
}

func NewQueue(size int) *Queue {
	return &Queue{ch: make(chan []ingest.Accepted, size)}
}

// Accept не ждёт: если очередь полна, SDK сразу получает 429.
func (q *Queue) Accept(_ context.Context, batch []ingest.Accepted) error {
	if q.closed.Load() {
		return ingest.ErrBusy
	}
	select {
	case q.ch <- batch:
		return nil
	default:
		return ingest.ErrBusy
	}
}

// Close вызывается после остановки HTTP-сервера: новых событий больше
// не будет, воркер дочитает остаток и выйдет.
func (q *Queue) Close() {
	if q.closed.CompareAndSwap(false, true) {
		close(q.ch)
	}
}

func (q *Queue) Len() int { return len(q.ch) }

// ---------- воркер ----------

type IssueStore interface {
	UpsertIssues(ctx context.Context, deltas []pg.IssueDelta) ([]pg.IssueResult, error)
}

type EventStore interface {
	InsertEvents(ctx context.Context, events []ch.Event) error
}

// Change — новая или вернувшаяся проблема: повод для уведомления.
type Change struct {
	Issue pg.IssueResult
	Event *event.Event
}

type Worker struct {
	Queue      *Queue
	Issues     IssueStore
	Events     EventStore
	OnChange   func(ctx context.Context, changes []Change) // может быть nil
	Log        *slog.Logger
	BatchSize  int
	FlushEvery time.Duration
	// Retries — сколько раз повторить запись в базу, прежде чем сдаться.
	Retries int

	Stats Stats
}

type Stats struct {
	Written atomic.Int64 // событий записано
	Dropped atomic.Int64 // событий потеряно после всех повторов
	Batches atomic.Int64
}

// Run работает, пока очередь не закрыта, и выходит, записав остаток.
// Контекст здесь не для остановки, а для отмены долгих повторов.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.FlushEvery)
	defer ticker.Stop()
	buf := make([]ingest.Accepted, 0, w.BatchSize)
	for {
		select {
		case batch, ok := <-w.Queue.ch:
			if !ok {
				w.flush(ctx, buf)
				return
			}
			buf = append(buf, batch...)
			if len(buf) >= w.BatchSize {
				w.flush(ctx, buf)
				buf = buf[:0]
			}
		case <-ticker.C:
			if len(buf) > 0 {
				w.flush(ctx, buf)
				buf = buf[:0]
			}
		}
	}
}

func (w *Worker) flush(ctx context.Context, batch []ingest.Accepted) {
	if len(batch) == 0 {
		return
	}
	w.Stats.Batches.Add(1)

	// 1. Группировка и сводка по проблемам.
	type key struct{ project, fp uint64 }
	groups := make([]grouping.Result, len(batch))
	deltas := map[key]*pg.IssueDelta{}
	firstEvent := map[key]*event.Event{}
	for i, a := range batch {
		g := grouping.Compute(a.Event)
		groups[i] = g
		k := key{a.ProjectID, g.Hash}
		ts := a.Event.Timestamp.Time
		d, ok := deltas[k]
		if !ok {
			d = &pg.IssueDelta{ProjectID: a.ProjectID, Fingerprint: g.Hash, Kind: g.Kind, FirstSeen: ts, LastSeen: ts}
			deltas[k] = d
			firstEvent[k] = a.Event
		}
		d.Count++
		if ts.Before(d.FirstSeen) {
			d.FirstSeen = ts
		}
		if !ts.Before(d.LastSeen) {
			// Заголовок и уровень берём у самого свежего события.
			d.LastSeen = ts
			d.Title, d.Culprit, d.Level, d.Platform = a.Event.Title(), a.Event.Culprit(), a.Event.Level, a.Event.Platform
		}
	}
	list := make([]pg.IssueDelta, 0, len(deltas))
	for _, d := range deltas {
		list = append(list, *d)
	}

	// 2. Проблемы в Postgres: нужны их id для событий.
	var results []pg.IssueResult
	err := w.retry(ctx, "postgres", func() error {
		var err error
		results, err = w.Issues.UpsertIssues(ctx, list)
		return err
	})
	if err != nil {
		w.drop(batch, "postgres", err)
		return
	}
	ids := make(map[key]uint64, len(results))
	var changes []Change
	for _, r := range results {
		k := key{r.ProjectID, r.Fingerprint}
		ids[k] = r.ID
		if r.Created || r.Regressed {
			changes = append(changes, Change{Issue: r, Event: firstEvent[k]})
		}
	}

	// 3. События в ClickHouse.
	rows := make([]ch.Event, len(batch))
	for i, a := range batch {
		rows[i] = toRow(a, ids[key{a.ProjectID, groups[i].Hash}])
	}
	if err := w.retry(ctx, "clickhouse", func() error { return w.Events.InsertEvents(ctx, rows) }); err != nil {
		w.drop(batch, "clickhouse", err)
		return
	}
	w.Stats.Written.Add(int64(len(batch)))

	if w.OnChange != nil && len(changes) > 0 {
		w.OnChange(ctx, changes)
	}
}

// retry повторяет запись с растущей паузой: база могла моргнуть
// (перезапуск, сеть), и терять из-за этого пачку обидно.
func (w *Worker) retry(ctx context.Context, what string, fn func() error) error {
	delay := 200 * time.Millisecond
	var err error
	for attempt := 0; attempt <= w.Retries; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if attempt == w.Retries {
			break
		}
		w.Log.Warn("запись не удалась, повторю", "store", what, "attempt", attempt+1, "err", err)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return err
		}
		delay *= 2
	}
	return err
}

func (w *Worker) drop(batch []ingest.Accepted, store string, err error) {
	w.Stats.Dropped.Add(int64(len(batch)))
	w.Log.Error("пачка событий потеряна", "store", store, "events", len(batch), "err", err)
}

func toRow(a ingest.Accepted, issueID uint64) ch.Event {
	e := a.Event
	tags := make(map[string]string, len(e.Tags))
	for _, kv := range e.Tags {
		tags[kv[0]] = kv[1]
	}
	sdk := ""
	if e.SDK != nil {
		sdk = e.SDK.Name
	}
	return ch.Event{
		ProjectID:   a.ProjectID,
		IssueID:     issueID,
		EventID:     e.EventID,
		Timestamp:   e.Timestamp.Time,
		ReceivedAt:  a.ReceivedAt,
		Level:       e.Level,
		Platform:    e.Platform,
		Environment: e.Environment,
		Release:     e.Release,
		SDK:         sdk,
		Title:       e.Title(),
		Culprit:     e.Culprit(),
		UserKey:     userKey(e.User, a.ClientIP),
		Tags:        tags,
		Data:        string(a.Raw),
	}
}

// userKey — чем отличать пользователей при подсчёте «сколько затронуто».
// "{{auto}}" в ip_address по протоколу значит «возьми IP запроса».
func userKey(u *event.User, clientIP string) string {
	if u == nil {
		return ""
	}
	switch {
	case u.ID != "":
		return "id:" + string(u.ID)
	case u.Email != "":
		return "email:" + strings.ToLower(u.Email)
	case u.Username != "":
		return "username:" + u.Username
	case u.IPAddress == "{{auto}}" && clientIP != "":
		return "ip:" + clientIP
	case u.IPAddress != "" && u.IPAddress != "{{auto}}":
		return "ip:" + u.IPAddress
	}
	return ""
}
