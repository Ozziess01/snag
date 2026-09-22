package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ozziess01/snag/internal/grouping"
)

// ---------- стенд ----------

type memProjects map[string]Project

func (m memProjects) ByKey(_ context.Context, key string) (Project, error) {
	if key == "db-down" {
		return Project{}, errors.New("connection refused")
	}
	p, ok := m[key]
	if !ok {
		return Project{}, ErrUnknownKey
	}
	return p, nil
}

type memSink struct {
	mu   sync.Mutex
	got  []Accepted
	busy bool
}

func (s *memSink) Accept(_ context.Context, batch []Accepted) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return ErrBusy
	}
	s.got = append(s.got, batch...)
	return nil
}

var received = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func newServer(t *testing.T, projects memProjects) (*http.ServeMux, *memSink) {
	t.Helper()
	sink := &memSink{}
	h := &Handler{
		Projects:       projects,
		Sink:           sink,
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:            func() time.Time { return received },
		MaxBodySize:    1 << 20,
		MaxDecodedSize: 2 << 20,
		MaxEventSize:   512 << 10,
	}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, sink
}

func do(mux http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// ---------- настоящие SDK ----------

// Фикстуры записаны от официальных SDK (tools/capture): каждый запрос
// сохранён байт в байт вместе с заголовками.
func TestRealSDKs(t *testing.T) {
	projects := memProjects{}
	for _, k := range []string{"node", "python", "gosdk", "php", "browser"} {
		projects[k] = Project{ID: 1}
	}
	mux, sink := newServer(t, projects)

	metas, _ := filepath.Glob("testdata/sdk/*.json")
	if len(metas) == 0 {
		t.Fatal("нет фикстур: запустите tools/capture")
	}
	for _, metaPath := range metas {
		name := strings.TrimSuffix(filepath.Base(metaPath), ".json")
		var meta struct {
			Method, Path, Query string
			Headers             map[string]string
		}
		raw, _ := os.ReadFile(metaPath)
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatal(err)
		}
		body, _ := os.ReadFile(strings.TrimSuffix(metaPath, ".json") + ".body")
		r := httptest.NewRequest(meta.Method, meta.Path+meta.Query, bytes.NewReader(body))
		for k, v := range meta.Headers {
			r.Header.Set(k, v)
		}
		w := do(mux, r)
		if w.Code != http.StatusOK {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}

	// Что должно было дойти от каждого SDK: заголовок события и уровень.
	got := map[string][]string{}
	for _, a := range sink.got {
		sdk := a.Event.SDK.Name
		got[sdk] = append(got[sdk], a.Event.Level+" | "+a.Event.Title()+" | "+a.Event.Culprit())
	}
	for sdk := range got {
		sort.Strings(got[sdk])
		t.Logf("%s:\n  %s", sdk, strings.Join(got[sdk], "\n  "))
	}

	want := map[string][]string{
		"sentry.javascript.node": {
			"TypeError: Cannot read properties of null (reading 'map')",
			"Error: не удалось прочитать конфиг",
			"RangeError: слишком большой заказ",
			"Order 17 failed",
		},
		"sentry.python": {
			"ValueError: invalid literal for int() with base 10: 'три'",
			"RuntimeError: не нашли настройку",
			"Order 17 failed",
		},
		"sentry.php": {
			"RuntimeException: User 7 not found",
			"LogicException: не удалось посчитать скидку",
			"Order 17 failed",
		},
		"sentry.go": {
			"Order 17 failed",
		},
		"sentry.javascript.browser": {
			"TypeError: Cannot read properties of undefined (reading 'items')",
			"Order 17 failed",
		},
	}
	for sdk, titles := range want {
		joined := strings.Join(got[sdk], "\n")
		for _, title := range titles {
			if !strings.Contains(joined, title) {
				t.Errorf("%s: нет события %q", sdk, title)
			}
		}
	}

	// В каждом приложении все ошибки разные, значит и групп столько же.
	groups := map[string]map[uint64]string{}
	for _, a := range sink.got {
		sdk := a.Event.SDK.Name
		if groups[sdk] == nil {
			groups[sdk] = map[uint64]string{}
		}
		g := grouping.Compute(a.Event)
		if prev, dup := groups[sdk][g.Hash]; dup {
			t.Errorf("%s: склеились разные ошибки %q и %q по %v", sdk, prev, a.Event.Title(), g.Parts)
		}
		groups[sdk][g.Hash] = a.Event.Title()
	}
}

// ---------- поведение обработчика ----------

func envelopeBody(event string) string {
	return `{"event_id":"aabbccddeeff00112233445566778899"}` + "\n" + `{"type":"event"}` + "\n" + event + "\n"
}

func post(path, body string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

func TestAuthAndRouting(t *testing.T) {
	mux, sink := newServer(t, memProjects{"k1": {ID: 1}, "k2": {ID: 2, AllowedOrigins: []string{"https://shop.example"}}})
	body := envelopeBody(`{"message":"hi"}`)
	auth := map[string]string{"X-Sentry-Auth": "Sentry sentry_key=k1, sentry_version=7"}

	tests := []struct {
		name string
		req  *http.Request
		code int
	}{
		{"ключ в заголовке", post("/api/1/envelope/", body, auth), 200},
		{"без косой черты в конце", post("/api/1/envelope", body, auth), 200},
		{"ключ в query", post("/api/1/envelope/?sentry_key=k1&sentry_version=7", body, nil), 200},
		{"ключ в dsn конверта", post("/api/1/envelope/", `{"dsn":"https://k1@snag.example/1"}`+"\n{\"type\":\"event\"}\n{}\n", nil), 200},
		{"без ключа", post("/api/1/envelope/", body, nil), 401},
		{"неизвестный ключ", post("/api/1/envelope/?sentry_key=nope", body, nil), 401},
		{"база проектов недоступна", post("/api/1/envelope/?sentry_key=db-down", body, nil), 503},
		{"ключ от другого проекта", post("/api/2/envelope/", body, auth), 403},
		{"разрешённый Origin", post("/api/2/envelope/?sentry_key=k2", body, map[string]string{"Origin": "https://shop.example"}), 200},
		{"чужой Origin", post("/api/2/envelope/?sentry_key=k2", body, map[string]string{"Origin": "https://evil.example"}), 403},
		{"битый конверт", post("/api/1/envelope/", "not an envelope", auth), 400},
		{"событие не объект", post("/api/1/envelope/", envelopeBody(`[1,2]`), auth), 400},
		{"store", post("/api/1/store/", `{"message":"old sdk"}`, auth), 200},
		{"GET не принимаем", httptest.NewRequest("GET", "/api/1/envelope/", nil), 405},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if w := do(mux, tt.req); w.Code != tt.code {
				t.Fatalf("код %d, ждали %d: %s", w.Code, tt.code, w.Body)
			}
		})
	}
	if len(sink.got) != 6 {
		t.Fatalf("принято событий %d, ждали 6", len(sink.got))
	}
}

func TestResponseID(t *testing.T) {
	mux, _ := newServer(t, memProjects{"k": {ID: 1}})
	w := do(mux, post("/api/1/envelope/?sentry_key=k", envelopeBody(`{"message":"hi"}`), nil))
	if strings.TrimSpace(w.Body.String()) != `{"id":"aabbccddeeff00112233445566778899"}` {
		t.Fatalf("ответ %s", w.Body)
	}
	// Конверт только с сессией: событий нет, отвечаем {}.
	w = do(mux, post("/api/1/envelope/?sentry_key=k", "{}\n{\"type\":\"session\"}\n{\"sid\":\"x\"}\n", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "{}" {
		t.Fatalf("сессия: %d %s", w.Code, w.Body)
	}
}

func TestCompression(t *testing.T) {
	mux, sink := newServer(t, memProjects{"k": {ID: 1}})

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(envelopeBody(`{"message":"сжатое"}`)))
	_ = zw.Close()

	r := httptest.NewRequest("POST", "/api/1/envelope/?sentry_key=k", bytes.NewReader(gz.Bytes()))
	r.Header.Set("Content-Encoding", "gzip")
	if w := do(mux, r); w.Code != 200 {
		t.Fatalf("gzip: %d %s", w.Code, w.Body)
	}
	if sink.got[0].Event.Title() != "сжатое" {
		t.Fatalf("title %q", sink.got[0].Event.Title())
	}

	r = post("/api/1/envelope/?sentry_key=k", "это не gzip", map[string]string{"Content-Encoding": "gzip"})
	if w := do(mux, r); w.Code != 400 {
		t.Fatalf("битый gzip: %d", w.Code)
	}
	r = post("/api/1/envelope/?sentry_key=k", "x", map[string]string{"Content-Encoding": "lzma"})
	if w := do(mux, r); w.Code != 400 {
		t.Fatalf("неизвестное сжатие: %d", w.Code)
	}
}

func TestSizeLimits(t *testing.T) {
	mux, _ := newServer(t, memProjects{"k": {ID: 1}})

	// «Zip-бомба»: 20 МБ нулей сжимаются в десятки килобайт.
	var bomb bytes.Buffer
	zw := gzip.NewWriter(&bomb)
	_, _ = zw.Write(bytes.Repeat([]byte{'0'}, 20<<20))
	_ = zw.Close()
	r := httptest.NewRequest("POST", "/api/1/envelope/?sentry_key=k", bytes.NewReader(bomb.Bytes()))
	r.Header.Set("Content-Encoding", "gzip")
	if w := do(mux, r); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("zip-бомба: %d (сжатая %d КБ)", w.Code, bomb.Len()>>10)
	}

	big := strings.Repeat("x", 2<<20)
	if w := do(mux, post("/api/1/envelope/?sentry_key=k", big, nil)); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("большое тело: %d", w.Code)
	}

	huge := envelopeBody(`{"message":"` + strings.Repeat("y", 600<<10) + `"}`)
	if w := do(mux, post("/api/1/envelope/?sentry_key=k", huge, nil)); w.Code != 400 {
		t.Fatalf("большое событие в конверте: %d", w.Code)
	}
}

func TestBusySink(t *testing.T) {
	mux, sink := newServer(t, memProjects{"k": {ID: 1}})
	sink.busy = true
	w := do(mux, post("/api/1/envelope/?sentry_key=k", envelopeBody(`{}`), nil))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" || w.Header().Get("X-Sentry-Rate-Limits") == "" {
		t.Fatalf("ждали 429 с Retry-After и X-Sentry-Rate-Limits: %d %v", w.Code, w.Header())
	}
}

func TestBrokenEnvelopeAcceptsNothing(t *testing.T) {
	mux, sink := newServer(t, memProjects{"k": {ID: 1}})
	body := "{}\n{\"type\":\"event\"}\n{\"message\":\"ok\"}\n{\"type\":\"event\"}\n[\"не объект\"]\n"
	if w := do(mux, post("/api/1/envelope/?sentry_key=k", body, nil)); w.Code != 400 {
		t.Fatalf("код %d", w.Code)
	}
	if len(sink.got) != 0 {
		t.Fatalf("из битого конверта приняли %d событий", len(sink.got))
	}
}

func TestCORS(t *testing.T) {
	mux, _ := newServer(t, memProjects{"k": {ID: 1}})
	r := httptest.NewRequest("OPTIONS", "/api/1/envelope/", nil)
	r.Header.Set("Origin", "https://shop.example")
	r.Header.Set("Access-Control-Request-Method", "POST")
	w := do(mux, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "https://shop.example" {
		t.Fatalf("preflight: %d %v", w.Code, w.Header())
	}

	r = post("/api/1/envelope/?sentry_key=k", envelopeBody(`{}`), map[string]string{"Origin": "https://shop.example", "Content-Type": "text/plain;charset=UTF-8"})
	w = do(mux, r)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Access-Control-Expose-Headers"), "Retry-After") {
		t.Fatalf("POST из браузера: %d %v", w.Code, w.Header())
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "[2001:db8::1]:5555"
	if ip := clientIP(r); ip != "2001:db8::1" {
		t.Errorf("IPv6: %q", ip)
	}
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	if ip := clientIP(r); ip != "203.0.113.7" {
		t.Errorf("X-Forwarded-For: %q", ip)
	}
}
