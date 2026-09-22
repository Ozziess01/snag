// Package pipeline — путь события от приёма до баз.
//
//	ingest ──► Queue ──► Worker: группировка ──► Postgres (проблемы)
//	                                         └─► ClickHouse (события)
//
// Приём кладёт события в очередь и сразу отвечает SDK. Воркер копит пачку
// (до BatchSize событий или FlushEvery времени) и пишет её в базы разом:
// один запрос в Postgres и одна вставка в ClickHouse на пачку, а не на
// каждое событие.
//
// Тяжёлая подготовка события — вычистка секретов, отпечаток, теги — идёт
// ещё в Accept, то есть в горутине HTTP-запроса: запросов много и они
// обрабатываются параллельно на всех ядрах. Воркер один, и ему остаётся
// только свести пачку и записать её. Замер показал, зачем: когда вычистка
// шла в воркере, он упирался в одно ядро на ~3 тыс. событий в секунду.
package pipeline

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Ozziess01/snag/internal/event"
	"github.com/Ozziess01/snag/internal/grouping"
	"github.com/Ozziess01/snag/internal/ingest"
	"github.com/Ozziess01/snag/internal/store"
)

// ---------- очередь ----------

// Queue — очередь запросов (каждый — пачка событий одного конверта).
// Живёт в памяти: при аварийном падении процесса недописанное теряется,
// при штатной остановке воркер дописывает всё. Место для постоянной
// очереди (Redis Streams, NATS) — за этим же интерфейсом ingest.Sink.
type Queue struct {
	ch     chan []prepared
	closed atomic.Bool
}

// prepared — событие, готовое к записи: отпечаток посчитан, строка для
// ClickHouse собрана (не хватает только id проблемы).
type prepared struct {
	accepted ingest.Accepted
	group    grouping.Result
	row      store.Event
}

func NewQueue(size int) *Queue {
	return &Queue{ch: make(chan []prepared, size)}
}

// Accept не ждёт: если очередь полна, SDK сразу получает 429. Полноту
// проверяем до подготовки, чтобы под перегрузкой не тратить процессор на
// события, которые всё равно отобьём.
func (q *Queue) Accept(_ context.Context, batch []ingest.Accepted) error {
	if q.closed.Load() || len(q.ch) == cap(q.ch) {
		return ingest.ErrBusy
	}
	items := make([]prepared, len(batch))
	for i, a := range batch {
		items[i] = prepared{accepted: a, group: grouping.Compute(a.Event), row: toRow(a)}
	}
	select {
	case q.ch <- items:
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
	UpsertIssues(ctx context.Context, deltas []store.IssueDelta) ([]store.IssueResult, error)
}

type EventStore interface {
	InsertEvents(ctx context.Context, events []store.Event) error
}

// Activity — что пачка сделала с одной проблемой: сколько событий пришло,
// появилась ли проблема впервые или вернулась. По этому уведомления
// решают, писать ли в Telegram (в том числе про всплески).
type Activity struct {
	Issue store.IssueResult
	Event *event.Event // самое свежее событие проблемы в пачке
	Count int
}

type Worker struct {
	Queue      *Queue
	Issues     IssueStore
	Events     EventStore
	OnFlush    func(ctx context.Context, activity []Activity) // может быть nil
	Log        *slog.Logger
	BatchSize  int
	FlushEvery time.Duration
	// Retries — сколько раз повторить запись в базу, прежде чем сдаться.
	Retries int
	// Workers — сколько пачек пишется одновременно. Запись в базы — в
	// основном ожидание сети, и пока одна пачка ждёт ClickHouse, другая
	// уже собирается. Postgres это переживает: строки проблем обновляются
	// в одном порядке, взаимных блокировок нет. 0 — один.
	Workers int

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
	n := max(w.Workers, 1)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.loop(ctx)
		}()
	}
	wg.Wait()
}

func (w *Worker) loop(ctx context.Context) {
	ticker := time.NewTicker(w.FlushEvery)
	defer ticker.Stop()
	buf := make([]prepared, 0, w.BatchSize)
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

func (w *Worker) flush(ctx context.Context, batch []prepared) {
	if len(batch) == 0 {
		return
	}
	w.Stats.Batches.Add(1)

	// 1. Сводка по проблемам: отпечатки уже посчитаны в Accept.
	type key struct{ project, fp uint64 }
	deltas := map[key]*store.IssueDelta{}
	lastEvent := map[key]*event.Event{}
	for _, p := range batch {
		a, g := p.accepted, p.group
		k := key{a.ProjectID, g.Hash}
		ts := a.Event.Timestamp.Time
		d, ok := deltas[k]
		if !ok {
			d = &store.IssueDelta{ProjectID: a.ProjectID, Fingerprint: g.Hash, Kind: g.Kind, FirstSeen: ts, LastSeen: ts}
			deltas[k] = d
		}
		d.Count++
		if ts.Before(d.FirstSeen) {
			d.FirstSeen = ts
		}
		if !ts.Before(d.LastSeen) {
			// Заголовок и уровень берём у самого свежего события.
			d.LastSeen = ts
			d.Title, d.Culprit, d.Level, d.Platform = a.Event.Title(), a.Event.Culprit(), a.Event.Level, a.Event.Platform
			lastEvent[k] = a.Event
		}
	}
	list := make([]store.IssueDelta, 0, len(deltas))
	for _, d := range deltas {
		list = append(list, *d)
	}

	// 2. Проблемы в Postgres: нужны их id для событий.
	var results []store.IssueResult
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
	activity := make([]Activity, 0, len(results))
	for _, r := range results {
		k := key{r.ProjectID, r.Fingerprint}
		ids[k] = r.ID
		activity = append(activity, Activity{Issue: r, Event: lastEvent[k], Count: int(deltas[k].Count)})
	}

	// 3. События в ClickHouse.
	rows := make([]store.Event, len(batch))
	for i, p := range batch {
		rows[i] = p.row
		rows[i].IssueID = ids[key{p.accepted.ProjectID, p.group.Hash}]
	}
	if err := w.retry(ctx, "clickhouse", func() error { return w.Events.InsertEvents(ctx, rows) }); err != nil {
		w.drop(batch, "clickhouse", err)
		return
	}
	w.Stats.Written.Add(int64(len(batch)))

	if w.OnFlush != nil {
		w.OnFlush(ctx, activity)
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

func (w *Worker) drop(batch []prepared, store string, err error) {
	w.Stats.Dropped.Add(int64(len(batch)))
	w.Log.Error("пачка событий потеряна", "store", store, "events", len(batch), "err", err)
}

func toRow(a ingest.Accepted) store.Event {
	e := a.Event
	sdk := ""
	if e.SDK != nil {
		sdk = e.SDK.Name
	}
	return store.Event{
		ProjectID:   a.ProjectID,
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
		Tags:        event.DerivedTags(e),
		Data:        string(event.Scrub(a.Raw)),
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
