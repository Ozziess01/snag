package api

import (
	"context"
	"errors"
	"testing"

	"github.com/Ozziess01/snag/internal/store"
)

type fakeNotifier struct {
	tested  []store.Channel
	forgot  []uint64
	failing bool
}

func (f *fakeNotifier) Test(_ context.Context, ch store.Channel, _ store.Project) error {
	if f.failing {
		return errors.New("Bad Request: chat not found")
	}
	f.tested = append(f.tested, ch)
	return nil
}

func (f *fakeNotifier) Forget(id uint64) { f.forgot = append(f.forgot, id) }

func TestChannels(t *testing.T) {
	e := newEnv(t, false)
	if code, _ := e.do("POST", "/api/v1/projects", map[string]string{"name": "Магазин"}); code != 201 {
		t.Fatal(code)
	}

	// Без токена бота: настройки говорят «выключено», проверка — 503.
	if _, body := e.do("GET", "/api/v1/settings", nil); body["telegram"].(map[string]any)["enabled"] != false {
		t.Fatalf("settings: %v", body)
	}

	code, body := e.do("POST", "/api/v1/projects/1/channels", map[string]any{"target": "-1001234567890", "on_new": true, "on_regression": true, "spike_threshold": 50})
	if code != 201 {
		t.Fatalf("создание: %d %v", code, body)
	}
	ch := body["channel"].(map[string]any)
	if ch["spike_window"] != float64(5) || ch["kind"] != "telegram" {
		t.Fatalf("значения по умолчанию: %v", ch)
	}
	id := itoa(uint64(ch["id"].(float64)))

	for name, req := range map[string]map[string]any{
		"не chat id":     {"target": "hello world"},
		"порог меньше 0": {"target": "42", "spike_threshold": -1},
		"окно больше 60": {"target": "42", "spike_window": 90},
	} {
		if code, body := e.do("POST", "/api/v1/projects/1/channels", req); code != 400 || body["error"] == "" {
			t.Errorf("%s: %d %v", name, code, body)
		}
	}
	if code, _ := e.do("POST", "/api/v1/projects/1/channels", map[string]any{"target": "-1001234567890"}); code != 409 {
		t.Errorf("тот же чат дважды: %d", code)
	}
	if code, _ := e.do("POST", "/api/v1/projects/99/channels", map[string]any{"target": "42"}); code != 404 {
		t.Errorf("чужой проект: %d", code)
	}

	code, body = e.do("PUT", "/api/v1/projects/1/channels/"+id, map[string]any{"target": "@snag_alerts", "on_new": false, "on_regression": true})
	if code != 200 || body["channel"].(map[string]any)["target"] != "@snag_alerts" || body["channel"].(map[string]any)["on_new"] != false {
		t.Fatalf("изменение: %d %v", code, body)
	}

	if code, _ := e.do("POST", "/api/v1/projects/1/channels/"+id+"/test", nil); code != 503 {
		t.Errorf("проверка без бота: %d", code)
	}

	_, body = e.do("GET", "/api/v1/projects/1/channels", nil)
	if list := body["channels"].([]any); len(list) != 1 {
		t.Fatalf("список: %v", body)
	}
	if code, _ := e.do("DELETE", "/api/v1/projects/1/channels/"+id, nil); code != 204 {
		t.Errorf("удаление: %d", code)
	}
	if code, _ := e.do("DELETE", "/api/v1/projects/1/channels/"+id, nil); code != 404 {
		t.Errorf("повторное удаление: %d", code)
	}
}

func TestChannelTestMessage(t *testing.T) {
	e := newEnv(t, false)
	fn := &fakeNotifier{}
	e.api.Notifier, e.api.BotName = fn, "snag_alerts_bot"
	e.do("POST", "/api/v1/projects", map[string]string{"name": "Магазин"})
	_, body := e.do("POST", "/api/v1/projects/1/channels", map[string]any{"target": "42", "on_new": true})
	id := itoa(uint64(body["channel"].(map[string]any)["id"].(float64)))

	if _, body := e.do("GET", "/api/v1/settings", nil); body["telegram"].(map[string]any)["bot"] != "snag_alerts_bot" {
		t.Fatalf("имя бота: %v", body)
	}
	if code, _ := e.do("POST", "/api/v1/projects/1/channels/"+id+"/test", nil); code != 200 || len(fn.tested) != 1 || fn.tested[0].Target != "42" {
		t.Fatalf("проверка: %d %+v", code, fn.tested)
	}
	if len(fn.forgot) == 0 {
		t.Error("после изменения каналов кеш уведомлений должен сбрасываться")
	}
	fn.failing = true
	if code, body := e.do("POST", "/api/v1/projects/1/channels/"+id+"/test", nil); code != 502 || body["error"] == nil {
		t.Fatalf("ошибка Telegram: %d %v", code, body)
	}
}
