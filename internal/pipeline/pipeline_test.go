package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Ozziess01/snag/internal/event"
	"github.com/Ozziess01/snag/internal/ingest"
	"github.com/Ozziess01/snag/internal/store"
)

type fakeIssues struct {
	mu        sync.Mutex
	calls     [][]store.IssueDelta
	failTimes int
	nextID    uint64
	known     map[uint64]uint64 // fingerprint → id
}

func (f *fakeIssues) UpsertIssues(_ context.Context, deltas []store.IssueDelta) ([]store.IssueResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failTimes > 0 {
		f.failTimes--
		return nil, errors.New("connection reset")
	}
	f.calls = append(f.calls, deltas)
	if f.known == nil {
		f.known = map[uint64]uint64{}
	}
	var out []store.IssueResult
	for _, d := range deltas {
		id, ok := f.known[d.Fingerprint]
		if !ok {
			f.nextID++
			id = f.nextID
			f.known[d.Fingerprint] = id
		}
		out = append(out, store.IssueResult{ID: id, ProjectID: d.ProjectID, Fingerprint: d.Fingerprint, Created: !ok})
	}
	return out, nil
}

type fakeEvents struct {
	mu   sync.Mutex
	rows []store.Event
	fail bool
}

func (f *fakeEvents) InsertEvents(_ context.Context, rows []store.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("clickhouse down")
	}
	f.rows = append(f.rows, rows...)
	return nil
}

func accepted(title string, ts time.Time) ingest.Accepted {
	return ingest.Accepted{
		ProjectID: 1,
		Event: &event.Event{
			EventID:   "aabbccddeeff00112233445566778899",
			Timestamp: event.Time{Time: ts},
			Level:     "error",
			Exception: event.ValuesOf[event.Exception]{{Type: title, Value: "v"}},
			User:      &event.User{ID: "42"},
		},
		Raw:        []byte(`{}`),
		ReceivedAt: ts,
	}
}

func newWorker(q *Queue, issues *fakeIssues, events *fakeEvents) *Worker {
	return &Worker{
		Queue: q, Issues: issues, Events: events,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		BatchSize: 100, FlushEvery: 20 * time.Millisecond, Retries: 2,
	}
}

func TestWorkerGroupsAndWrites(t *testing.T) {
	q := NewQueue(10)
	issues, events := &fakeIssues{}, &fakeEvents{}
	w := newWorker(q, issues, events)
	var activity []Activity
	w.OnFlush = func(_ context.Context, a []Activity) { activity = append(activity, a...) }

	t0 := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	_ = q.Accept(context.Background(), []ingest.Accepted{accepted("TypeError", t0), accepted("TypeError", t0.Add(time.Second))})
	_ = q.Accept(context.Background(), []ingest.Accepted{accepted("RangeError", t0)})
	q.Close()
	w.Run(context.Background())

	if len(issues.calls) != 1 || len(issues.calls[0]) != 2 {
		t.Fatalf("ждали один запрос в Postgres с двумя проблемами: %+v", issues.calls)
	}
	for _, d := range issues.calls[0] {
		if d.Title == "TypeError: v" && (d.Count != 2 || !d.LastSeen.Equal(t0.Add(time.Second))) {
			t.Errorf("сводка по TypeError: %+v", d)
		}
	}
	if len(events.rows) != 3 {
		t.Fatalf("в ClickHouse %d событий, ждали 3", len(events.rows))
	}
	if events.rows[0].IssueID != events.rows[1].IssueID || events.rows[0].IssueID == events.rows[2].IssueID {
		t.Errorf("issue_id у событий: %d %d %d", events.rows[0].IssueID, events.rows[1].IssueID, events.rows[2].IssueID)
	}
	if events.rows[0].UserKey != "id:42" || events.rows[0].EventID == "" {
		t.Errorf("строка события: %+v", events.rows[0])
	}
	if len(activity) != 2 || w.Stats.Written.Load() != 3 {
		t.Fatalf("проблем в сводке %d, записано %d", len(activity), w.Stats.Written.Load())
	}
	for _, a := range activity {
		if !a.Issue.Created || a.Event == nil {
			t.Errorf("обе проблемы новые и с событием: %+v", a)
		}
		if a.Event.Title() == "TypeError: v" && (a.Count != 2 || !a.Event.Timestamp.Equal(t0.Add(time.Second))) {
			t.Errorf("TypeError: ждали 2 события и самое свежее, получили %d и %v", a.Count, a.Event.Timestamp)
		}
	}
}

func TestWorkerRetriesThenSucceeds(t *testing.T) {
	q := NewQueue(10)
	issues, events := &fakeIssues{failTimes: 2}, &fakeEvents{}
	w := newWorker(q, issues, events)
	_ = q.Accept(context.Background(), []ingest.Accepted{accepted("E", time.Now())})
	q.Close()
	w.Run(context.Background())
	if len(events.rows) != 1 || w.Stats.Dropped.Load() != 0 {
		t.Fatalf("после двух сбоев запись должна пройти: rows=%d dropped=%d", len(events.rows), w.Stats.Dropped.Load())
	}
}

func TestWorkerDropsAfterRetries(t *testing.T) {
	q := NewQueue(10)
	issues, events := &fakeIssues{}, &fakeEvents{fail: true}
	w := newWorker(q, issues, events)
	w.Retries = 1
	_ = q.Accept(context.Background(), []ingest.Accepted{accepted("E", time.Now()), accepted("E", time.Now())})
	q.Close()
	w.Run(context.Background())
	if w.Stats.Dropped.Load() != 2 {
		t.Fatalf("dropped=%d", w.Stats.Dropped.Load())
	}
}

func TestWorkerFlushesByTimer(t *testing.T) {
	q := NewQueue(10)
	issues, events := &fakeIssues{}, &fakeEvents{}
	w := newWorker(q, issues, events)
	done := make(chan struct{})
	go func() { w.Run(context.Background()); close(done) }()

	_ = q.Accept(context.Background(), []ingest.Accepted{accepted("E", time.Now())})
	deadline := time.Now().Add(2 * time.Second)
	for w.Stats.Written.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if w.Stats.Written.Load() != 1 {
		t.Fatal("пачка меньше BatchSize должна записаться по таймеру")
	}
	q.Close()
	<-done
}

func TestQueueFull(t *testing.T) {
	q := NewQueue(1)
	if err := q.Accept(context.Background(), []ingest.Accepted{{}}); err != nil {
		t.Fatal(err)
	}
	if err := q.Accept(context.Background(), []ingest.Accepted{{}}); !errors.Is(err, ingest.ErrBusy) {
		t.Fatalf("полная очередь: %v", err)
	}
	q.Close()
	q.Close() // второй Close не паникует
	if err := q.Accept(context.Background(), []ingest.Accepted{{}}); !errors.Is(err, ingest.ErrBusy) {
		t.Fatalf("закрытая очередь: %v", err)
	}
}

func TestUserKey(t *testing.T) {
	tests := []struct {
		u    *event.User
		ip   string
		want string
	}{
		{nil, "1.2.3.4", ""},
		{&event.User{ID: "7", Email: "a@b.c"}, "", "id:7"},
		{&event.User{Email: "Bob@Example.com"}, "", "email:bob@example.com"},
		{&event.User{IPAddress: "{{auto}}"}, "203.0.113.5", "ip:203.0.113.5"},
		{&event.User{IPAddress: "{{auto}}"}, "", ""},
		{&event.User{IPAddress: "10.0.0.1"}, "203.0.113.5", "ip:10.0.0.1"},
	}
	for _, tt := range tests {
		if got := userKey(tt.u, tt.ip); got != tt.want {
			t.Errorf("%+v: %q, ждали %q", tt.u, got, tt.want)
		}
	}
}
