package event

import (
	"strings"
	"testing"
	"time"
)

func TestDecodeShapes(t *testing.T) {
	// Одни и те же данные в «старой» и «новой» форме должны дать одно и то же.
	forms := map[string]string{
		"новая форма": `{
			"event_id": "9ec79c33ec9942ab8353589fcb2e04dc",
			"timestamp": "2026-09-20T10:00:00.5Z",
			"exception": {"values": [{"type": "ValueError", "value": "bad", "stacktrace": {"frames": [{"filename": "app.py", "function": "run", "lineno": 3, "in_app": true}]}}]},
			"tags": {"browser": "Chrome", "retries": 3},
			"user": {"id": 42},
			"breadcrumbs": {"values": [{"message": "click", "timestamp": 1790000000}]},
			"message": {"message": "User %s not found", "params": ["5"], "formatted": "User 5 not found"}
		}`,
		"старая форма": `{
			"event_id": "9ec79c33-ec99-42ab-8353-589fcb2e04dc",
			"timestamp": 1789898400.5,
			"exception": [{"type": "ValueError", "value": "bad", "stacktrace": {"frames": [{"filename": "app.py", "function": "run", "lineno": 3, "in_app": true}]}}],
			"tags": [["browser", "Chrome"], ["retries", "3"]],
			"user": {"id": "42"},
			"breadcrumbs": [{"message": "click", "timestamp": "2026-09-21T22:13:20Z"}],
			"logentry": {"message": "User %s not found", "formatted": "User 5 not found"}
		}`,
	}
	received := time.Date(2026, 9, 20, 10, 0, 5, 0, time.UTC)
	for name, js := range forms {
		t.Run(name, func(t *testing.T) {
			e, err := Decode([]byte(js))
			if err != nil {
				t.Fatal(err)
			}
			Normalize(e, "", received)
			if e.EventID != "9ec79c33ec9942ab8353589fcb2e04dc" {
				t.Errorf("event_id %q", e.EventID)
			}
			if want := time.Date(2026, 9, 20, 10, 0, 0, 500_000_000, time.UTC); !e.Timestamp.Equal(want) {
				t.Errorf("timestamp %v, ждали %v", e.Timestamp, want)
			}
			if got := e.Title(); got != "ValueError: bad" {
				t.Errorf("title %q", got)
			}
			if got := e.Culprit(); got != "app.py in run" {
				t.Errorf("culprit %q", got)
			}
			if v, _ := e.Tags.Get("retries"); v != "3" {
				t.Errorf("tag retries %q", v)
			}
			if e.User == nil || e.User.ID != "42" {
				t.Errorf("user.id %+v", e.User)
			}
			if len(e.Breadcrumbs) != 1 || e.Breadcrumbs[0].Message != "click" {
				t.Errorf("breadcrumbs %+v", e.Breadcrumbs)
			}
		})
	}
}

func TestDecodeKeepsEventWhenFieldIsBroken(t *testing.T) {
	// lineno строкой, timestamp словом, tags числом: событие всё равно принимаем.
	e, err := Decode([]byte(`{
		"timestamp": "yesterday",
		"tags": 5,
		"user": {"id": {"nested": true}},
		"exception": {"values": [{"type": "E", "value": "v", "stacktrace": {"frames": [{"function": "f", "lineno": "12"}]}}]}
	}`))
	if err != nil {
		t.Fatalf("событие не должно отбрасываться: %v", err)
	}
	if e.Primary() == nil || e.Primary().Type != "E" || len(e.Primary().Stacktrace.Frames) != 1 {
		t.Fatalf("исключение потерялось: %+v", e.Exception)
	}
	if e.Primary().Stacktrace.Frames[0].Function != "f" {
		t.Fatalf("кадр разобран неверно: %+v", e.Primary().Stacktrace.Frames[0])
	}
}

func TestDecodeRejectsNonObject(t *testing.T) {
	for _, in := range []string{"", "[]", `"text"`, "null", "{broken"} {
		if _, err := Decode([]byte(in)); err == nil {
			t.Errorf("%q: ждали ошибку", in)
		}
	}
}

func TestNormalize(t *testing.T) {
	received := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	t.Run("время из будущего и древнее заменяется временем получения", func(t *testing.T) {
		for _, ts := range []time.Time{received.Add(time.Hour), received.AddDate(0, -2, 0), {}} {
			e := &Event{Timestamp: Time{ts}}
			Normalize(e, "", received)
			if !e.Timestamp.Equal(received) {
				t.Errorf("%v → %v", ts, e.Timestamp)
			}
		}
		e := &Event{Timestamp: Time{received.Add(-time.Hour)}}
		Normalize(e, "", received)
		if !e.Timestamp.Equal(received.Add(-time.Hour)) {
			t.Errorf("нормальное время не должно меняться: %v", e.Timestamp)
		}
	})

	t.Run("event_id", func(t *testing.T) {
		e := &Event{EventID: "not-an-id"}
		Normalize(e, "AABBCCDDEEFF00112233445566778899", received)
		if e.EventID != "aabbccddeeff00112233445566778899" {
			t.Errorf("должен взять id из конверта: %q", e.EventID)
		}
		e = &Event{}
		Normalize(e, "", received)
		if len(e.EventID) != 32 {
			t.Errorf("должен сгенерировать id: %q", e.EventID)
		}
	})

	t.Run("уровни", func(t *testing.T) {
		for in, want := range map[string]string{"": "error", "WARN": "warning", "critical": "fatal", "log": "info", "trace": "debug", "whatever": "error"} {
			e := &Event{Level: in}
			Normalize(e, "", received)
			if e.Level != want {
				t.Errorf("%q → %q, ждали %q", in, e.Level, want)
			}
		}
	})

	t.Run("длинные поля режутся по символам", func(t *testing.T) {
		e := &Event{
			Exception: ValuesOf[Exception]{{Value: strings.Repeat("я", maxTextLen)}},
			Tags:      Pairs{{"k", strings.Repeat("ж", 500)}},
		}
		Normalize(e, "", received)
		v := e.Exception[0].Value
		if len(v) > maxTextLen+len("…") || !strings.HasSuffix(v, "…") {
			t.Errorf("value не обрезан: %d байт", len(v))
		}
		if strings.ContainsRune(v, '�') || strings.ContainsRune(e.Tags[0][1], '�') {
			t.Error("обрезка сломала UTF-8")
		}
	})

	t.Run("слишком длинный стек: остаются кадры у места падения", func(t *testing.T) {
		frames := make([]Frame, maxFrames+50)
		frames[len(frames)-1].Function = "crash_here"
		e := &Event{Exception: ValuesOf[Exception]{{Stacktrace: &Stacktrace{Frames: frames}}}}
		Normalize(e, "", received)
		got := e.Exception[0].Stacktrace.Frames
		if len(got) != maxFrames || got[len(got)-1].Function != "crash_here" {
			t.Errorf("кадров %d, последний %q", len(got), got[len(got)-1].Function)
		}
	})
}

func TestTitleAndCulprit(t *testing.T) {
	yes, no := true, false
	e := &Event{Exception: ValuesOf[Exception]{
		{Type: "IOError", Value: "первопричина"},
		{Type: "RuntimeError", Value: "обёртка\nвторая строка", Stacktrace: &Stacktrace{Frames: []Frame{
			{Filename: "app/handler.go", Function: "Serve", InApp: &yes},
			{Filename: "net/http/server.go", Function: "ServeHTTP", InApp: &no},
		}}},
	}}
	if got := e.Title(); got != "RuntimeError: обёртка" {
		t.Errorf("title %q", got)
	}
	if got := e.Culprit(); got != "app/handler.go in Serve" {
		t.Errorf("culprit должен указывать на код приложения, а не на net/http: %q", got)
	}

	msg := &Event{LogEntry: &Message{Message: "Order %s failed", Formatted: "Order 17 failed"}}
	if msg.Title() != "Order 17 failed" || msg.LogEntry.Template() != "Order %s failed" {
		t.Errorf("message: %q / %q", msg.Title(), msg.LogEntry.Template())
	}
	anon := &Event{Exception: ValuesOf[Exception]{{Type: "E", Stacktrace: &Stacktrace{Frames: []Frame{{Filename: "app.mjs", Function: "?"}}}}}}
	if got := anon.Culprit(); got != "app.mjs" {
		t.Errorf("анонимная функция: %q", got)
	}
	url := &Event{Exception: ValuesOf[Exception]{{Type: "E", Stacktrace: &Stacktrace{Frames: []Frame{{Filename: "https://shop.example/assets/app.js?v=3", Function: "render"}}}}}}
	if got := url.Culprit(); got != "/assets/app.js in render" {
		t.Errorf("URL в culprit: %q", got)
	}
	if (&Event{}).Title() != "<без названия>" {
		t.Error("пустое событие")
	}
}
