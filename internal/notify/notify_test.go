package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ozziess01/snag/internal/event"
	"github.com/Ozziess01/snag/internal/pipeline"
	"github.com/Ozziess01/snag/internal/store"
	"github.com/Ozziess01/snag/internal/store/mem"
)

type sent struct{ chat, text string }

type fakeSender struct {
	mu   sync.Mutex
	msgs []sent
}

func (f *fakeSender) Send(_ context.Context, chat, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, sent{chat, text})
	return nil
}

func setup(t *testing.T, ch store.Channel) (*Notifier, *fakeSender, store.Project, *time.Time) {
	t.Helper()
	st := mem.New()
	p, _ := st.CreateProject(context.Background(), "Магазин <A&B>")
	ch.ProjectID = p.ID
	if _, err := st.SaveChannel(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSender{}
	n := New(st, fs, "https://snag.example/", slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	n.Now = func() time.Time { return now }
	return n, fs, p, &now
}

func act(project, issue uint64, title string, count int, created, regressed bool) pipeline.Activity {
	typ, val, _ := strings.Cut(title, ": ")
	return pipeline.Activity{
		Issue: store.IssueResult{ID: issue, ProjectID: project, Created: created, Regressed: regressed},
		Event: &event.Event{Exception: event.ValuesOf[event.Exception]{{Type: typ, Value: val}}, Environment: "production", Release: "shop@2.0"},
		Count: count,
	}
}

// drain отправляет всё, что накопилось в очереди.
func drain(n *Notifier) {
	for {
		select {
		case m := <-n.queue:
			_ = n.Sender.Send(context.Background(), m.chatID, m.text)
		default:
			return
		}
	}
}

func TestNewAndRegressed(t *testing.T) {
	n, fs, p, _ := setup(t, store.Channel{Target: "-100123", OnNew: true, OnRegression: true})
	n.Handle(context.Background(), []pipeline.Activity{
		act(p.ID, 7, "TypeError: x is <undefined>", 3, true, false),
		act(p.ID, 8, "ValueError: old", 1, false, true),
		act(p.ID, 9, "E: просто повтор", 5, false, false),
	})
	drain(n)
	if len(fs.msgs) != 2 {
		t.Fatalf("ждали 2 сообщения (новая и вернувшаяся), получили %d: %+v", len(fs.msgs), fs.msgs)
	}
	newMsg := fs.msgs[0].text
	for _, want := range []string{"Новая проблема · Магазин &lt;A&amp;B&gt;", "<b>TypeError</b>: x is &lt;undefined&gt;", "production · shop@2.0", `href="https://snag.example/#/issues/7"`} {
		if !strings.Contains(newMsg, want) {
			t.Errorf("в сообщении нет %q:\n%s", want, newMsg)
		}
	}
	if !strings.Contains(fs.msgs[1].text, "Проблема вернулась") || fs.msgs[1].chat != "-100123" {
		t.Errorf("регрессия: %+v", fs.msgs[1])
	}
}

func TestManyNewIssuesAreSummarized(t *testing.T) {
	n, fs, p, _ := setup(t, store.Channel{Target: "42", OnNew: true})
	var acts []pipeline.Activity
	for i := uint64(1); i <= 12; i++ {
		acts = append(acts, act(p.ID, i, fmt.Sprintf("E%d: boom", i), 1, true, false))
	}
	n.Handle(context.Background(), acts)
	drain(n)
	if len(fs.msgs) != 1 {
		t.Fatalf("12 новых проблем — одно сообщение, получили %d", len(fs.msgs))
	}
	if m := fs.msgs[0].text; !strings.Contains(m, "12 новых проблем") || !strings.Contains(m, "и ещё 7") {
		t.Errorf("сводка:\n%s", m)
	}
}

func TestChannelSettingsAreRespected(t *testing.T) {
	n, fs, p, _ := setup(t, store.Channel{Target: "42", OnNew: false, OnRegression: false})
	n.Handle(context.Background(), []pipeline.Activity{act(p.ID, 1, "E: x", 1, true, false), act(p.ID, 2, "E: y", 1, false, true)})
	drain(n)
	if len(fs.msgs) != 0 {
		t.Fatalf("уведомления выключены, а пришло: %+v", fs.msgs)
	}
}

func TestSpike(t *testing.T) {
	n, fs, p, now := setup(t, store.Channel{Target: "42", SpikeThreshold: 50, SpikeWindow: 5})
	send := func(count int) {
		n.Handle(context.Background(), []pipeline.Activity{act(p.ID, 3, "TimeoutError: gateway", count, false, false)})
		drain(n)
	}
	send(20)
	*now = now.Add(time.Minute)
	send(20)
	if len(fs.msgs) != 0 {
		t.Fatalf("40 событий за 2 минуты — ещё не всплеск: %+v", fs.msgs)
	}
	*now = now.Add(time.Minute)
	send(15)
	if len(fs.msgs) != 1 || !strings.Contains(fs.msgs[0].text, "55 событий за 5 мин") {
		t.Fatalf("55 за 5 минут — всплеск: %+v", fs.msgs)
	}
	*now = now.Add(time.Minute)
	send(100)
	if len(fs.msgs) != 1 {
		t.Fatal("в течение часа повторно о том же всплеске не пишем")
	}
	*now = now.Add(61 * time.Minute)
	send(60)
	if len(fs.msgs) != 2 {
		t.Fatalf("через час — снова можно: %d", len(fs.msgs))
	}
	// Старые минуты выпадают из окна: 60 событий час назад не считаются.
	*now = now.Add(10 * time.Minute)
	send(1)
	if len(fs.msgs) != 2 {
		t.Fatal("вне окна не всплеск")
	}
}

func TestTelegramClient(t *testing.T) {
	var got map[string]any
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.HasPrefix(r.URL.Path, "/botSECRET-TOKEN/") {
			t.Errorf("путь %s", r.URL.Path)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage") && calls == 1:
			_ = json.NewDecoder(r.Body).Decode(&got)
			fmt.Fprint(w, `{"ok":true,"result":{}}`)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			w.WriteHeader(429)
			fmt.Fprint(w, `{"ok":false,"description":"Too Many Requests","parameters":{"retry_after":3}}`)
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			fmt.Fprint(w, `{"ok":true,"result":{"username":"snag_alerts_bot"}}`)
		}
	}))
	defer srv.Close()

	tg := &Telegram{Token: "SECRET-TOKEN", BaseURL: srv.URL}
	if err := tg.Send(context.Background(), "-100", "<b>привет</b>"); err != nil {
		t.Fatal(err)
	}
	if got["chat_id"] != "-100" || got["parse_mode"] != "HTML" || got["text"] != "<b>привет</b>" {
		t.Errorf("запрос: %v", got)
	}
	var ra RetryAfter
	if err := tg.Send(context.Background(), "-100", "x"); !errors.As(err, &ra) || ra.Wait != 3*time.Second {
		t.Errorf("429: %v", err)
	}
	if name, err := tg.Username(context.Background()); err != nil || name != "snag_alerts_bot" {
		t.Errorf("getMe: %q %v", name, err)
	}
}

func TestTokenNeverLeaksIntoErrors(t *testing.T) {
	tg := &Telegram{Token: "123456:SECRET", BaseURL: "http://127.0.0.1:1"} // порт закрыт
	err := tg.Send(context.Background(), "1", "x")
	if err == nil || strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), "***") {
		t.Fatalf("токен в ошибке: %v", err)
	}
}
