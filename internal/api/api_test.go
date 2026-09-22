package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ozziess01/snag/internal/ingest"
	"github.com/Ozziess01/snag/internal/pipeline"
	"github.com/Ozziess01/snag/internal/store"
	"github.com/Ozziess01/snag/internal/store/mem"
)

// env — Snag целиком (приём, очередь, воркер, API) поверх хранилища в памяти.
type env struct {
	t      *testing.T
	mux    *http.ServeMux
	store  *mem.Store
	worker *pipeline.Worker
	cookie *http.Cookie
}

func newEnv(t *testing.T, withAuth bool) *env {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := mem.New()
	q := pipeline.NewQueue(100)
	w := &pipeline.Worker{Queue: q, Issues: st, Events: st, Log: log, BatchSize: 500, FlushEvery: 5 * time.Millisecond}
	done := make(chan struct{})
	go func() { w.Run(context.Background()); close(done) }()
	t.Cleanup(func() { q.Close(); <-done })

	mux := http.NewServeMux()
	(&ingest.Handler{Projects: st, Sink: q, Log: log, MaxBodySize: 1 << 20, MaxDecodedSize: 4 << 20, MaxEventSize: 1 << 20}).Register(mux)
	a := &API{Backend: st, Log: log, PublicURL: "https://snag.example"}
	if withAuth {
		a.Auth = st
	}
	a.Register(mux)
	return &env{t: t, mux: mux, store: st, worker: w}
}

func (e *env) do(method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	if method != http.MethodGet {
		r.Header.Set("X-Requested-With", "snag")
	}
	if e.cookie != nil {
		r.AddCookie(e.cookie)
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			e.cookie = c
		}
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// sendFixtures отправляет записанные запросы SDK в проект и ждёт, пока
// воркер запишет все события.
func (e *env) sendFixtures(project store.Project) int {
	e.t.Helper()
	metas, _ := filepath.Glob("../ingest/testdata/sdk/*.json")
	for _, m := range metas {
		var meta struct {
			Method, Path, Query string
			Headers             map[string]string
		}
		raw, _ := os.ReadFile(m)
		_ = json.Unmarshal(raw, &meta)
		body, _ := os.ReadFile(strings.TrimSuffix(m, ".json") + ".body")
		// Фикстуры записаны для проекта 1 с ключом по имени SDK: подменяем
		// ключ и проект на настоящие.
		r := httptest.NewRequest(meta.Method, "/api/"+itoa(project.ID)+"/envelope/?sentry_key="+project.PublicKey, bytes.NewReader(body))
		for k, v := range meta.Headers {
			if k != "x-sentry-auth" && k != "origin" {
				r.Header.Set(k, v)
			}
		}
		w := httptest.NewRecorder()
		e.mux.ServeHTTP(w, r)
		if w.Code != 200 {
			e.t.Fatalf("%s: %d %s", m, w.Code, w.Body)
		}
	}
	const want = 17 // событий в фикстурах (сессии не считаются)
	deadline := time.Now().Add(3 * time.Second)
	for e.worker.Stats.Written.Load() < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return int(e.worker.Stats.Written.Load())
}

func itoa(n uint64) string { b, _ := json.Marshal(n); return string(b) }

func TestAuthFlow(t *testing.T) {
	e := newEnv(t, true)
	if _, err := e.store.CreateUser(context.Background(), "admin@example.com", "correct horse"); err != nil {
		t.Fatal(err)
	}

	if code, _ := e.do("GET", "/api/v1/projects", nil); code != 401 {
		t.Fatalf("без входа: %d", code)
	}
	if code, _ := e.do("POST", "/api/v1/auth/login", map[string]string{"email": "admin@example.com", "password": "wrong"}); code != 401 {
		t.Fatalf("неверный пароль: %d", code)
	}
	code, body := e.do("POST", "/api/v1/auth/login", map[string]string{"email": " Admin@Example.com", "password": "correct horse"})
	if code != 200 || e.cookie == nil || !e.cookie.HttpOnly || e.cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("вход: %d %v %+v", code, body, e.cookie)
	}
	if code, body := e.do("GET", "/api/v1/auth/me", nil); code != 200 || body["user"].(map[string]any)["email"] != "admin@example.com" {
		t.Fatalf("me: %d %v", code, body)
	}

	// Без заголовка X-Requested-With изменения не принимаются (CSRF).
	r := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(`{"name":"x"}`))
	r.AddCookie(e.cookie)
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("POST без CSRF-заголовка: %d", w.Code)
	}

	if code, _ := e.do("POST", "/api/v1/auth/logout", nil); code != 204 {
		t.Fatalf("выход: %d", code)
	}
	if code, _ := e.do("GET", "/api/v1/auth/me", nil); code != 401 {
		t.Fatalf("после выхода: %d", code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t, true)
	_, _ = e.store.CreateUser(context.Background(), "a@b.c", "correct horse")
	for i := 0; i < maxLoginFails; i++ {
		e.do("POST", "/api/v1/auth/login", map[string]string{"email": "a@b.c", "password": "nope"})
	}
	if code, _ := e.do("POST", "/api/v1/auth/login", map[string]string{"email": "a@b.c", "password": "correct horse"}); code != 429 {
		t.Fatalf("после 5 ошибок вход должен быть закрыт даже с верным паролем: %d", code)
	}
}

func TestIssuesFromRealSDKs(t *testing.T) {
	e := newEnv(t, false)
	code, body := e.do("POST", "/api/v1/projects", map[string]string{"name": "Магазин"})
	if code != 201 {
		t.Fatalf("создание проекта: %d %v", code, body)
	}
	pj := body["project"].(map[string]any)
	if !strings.HasPrefix(pj["dsn"].(string), "https://") || !strings.HasSuffix(pj["dsn"].(string), "@snag.example/1") {
		t.Fatalf("DSN: %v", pj["dsn"])
	}
	project, _ := e.store.Project(context.Background(), 1)
	if n := e.sendFixtures(project); n != 17 {
		t.Fatalf("записано %d событий", n)
	}

	_, body = e.do("GET", "/api/v1/projects/1/issues?period=24h&sort=events", nil)
	issues := body["issues"].([]any)
	if len(issues) < 12 {
		t.Fatalf("проблем %d: %v", len(issues), body)
	}
	top := issues[0].(map[string]any)
	if top["title"] != "Order 17 failed" || top["events"] != float64(5) || len(top["buckets"].([]any)) != 24 {
		t.Fatalf("самая частая проблема — сообщение от пяти SDK: %v", top)
	}

	_, body = e.do("GET", "/api/v1/projects/1/issues?q=user+7", nil)
	if list := body["issues"].([]any); len(list) != 1 || !strings.Contains(list[0].(map[string]any)["title"].(string), "User 7 not found") {
		t.Fatalf("поиск: %v", body)
	}
	phpIssue := uint64(body["issues"].([]any)[0].(map[string]any)["id"].(float64))

	// Карточка проблемы и последнее событие со стеком и кодом.
	_, body = e.do("GET", "/api/v1/issues/"+itoa(phpIssue), nil)
	if body["stats_24h"].(map[string]any)["events"] != float64(1) || body["project"] == nil {
		t.Fatalf("карточка: %v", body)
	}
	tags := map[string]bool{}
	for _, tg := range body["tags"].([]any) {
		tags[tg.(map[string]any)["key"].(string)] = true
	}
	for _, k := range []string{"environment", "release", "level"} {
		if !tags[k] {
			t.Errorf("нет тега %s: %v", k, tags)
		}
	}

	_, body = e.do("GET", "/api/v1/issues/"+itoa(phpIssue)+"/events/latest", nil)
	ev := body["event"].(map[string]any)
	exc := ev["exceptions"].([]any)[0].(map[string]any)
	if exc["type"] != "RuntimeException" || len(exc["frames"].([]any)) == 0 {
		t.Fatalf("исключение: %v", exc)
	}
	var withCode bool
	for _, f := range exc["frames"].([]any) {
		if len(f.(map[string]any)["context"].([]any)) > 0 {
			withCode = true
		}
	}
	if !withCode {
		t.Error("у кадров PHP должен быть код вокруг строки")
	}
	if ev["raw"] == nil || body["older"] != "" || body["newer"] != "" {
		t.Errorf("raw / соседи: %v %v", body["older"], body["newer"])
	}

	// Решили проблему — пропала из открытых; статус проверяется.
	if code, _ := e.do("PUT", "/api/v1/issues/"+itoa(phpIssue), map[string]string{"status": "resolved"}); code != 200 {
		t.Fatalf("resolve: %d", code)
	}
	if code, _ := e.do("PUT", "/api/v1/issues/"+itoa(phpIssue), map[string]string{"status": "deleted"}); code != 400 {
		t.Fatalf("неизвестный статус: %d", code)
	}
	_, body = e.do("GET", "/api/v1/projects/1/issues?q=user+7", nil)
	if len(body["issues"].([]any)) != 0 {
		t.Fatalf("решённая проблема в открытых: %v", body)
	}
	_, body = e.do("GET", "/api/v1/projects/1/issues?q=user+7&status=resolved", nil)
	if len(body["issues"].([]any)) != 1 {
		t.Fatalf("решённая проблема в решённых: %v", body)
	}

	_, body = e.do("GET", "/api/v1/projects", nil)
	if p := body["projects"].([]any)[0].(map[string]any); p["open_issues"].(float64) < 11 {
		t.Fatalf("открытых проблем: %v", p)
	}
}

func TestBadRequests(t *testing.T) {
	e := newEnv(t, false)
	for path, want := range map[string]int{
		"/api/v1/projects/abc/issues":          400,
		"/api/v1/projects/1/issues?status=foo": 400,
		"/api/v1/issues/999":                   404,
		"/api/v1/issues/0":                     400,
	} {
		if code, _ := e.do("GET", path, nil); code != want {
			t.Errorf("%s: %d, ждали %d", path, code, want)
		}
	}
	if code, _ := e.do("POST", "/api/v1/projects", map[string]string{"name": "  "}); code != 400 {
		t.Errorf("пустое имя проекта: %d", code)
	}
}
