// Package notify — уведомления о проблемах в Telegram.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Telegram — клиент Bot API: отправка сообщений и чтение входящих.
type Telegram struct {
	Token   string
	BaseURL string // https://api.telegram.org; в тестах — фейковый сервер
	Client  *http.Client
}

// RetryAfter — Telegram попросил подождать (слишком частые сообщения).
type RetryAfter struct{ Wait time.Duration }

func (e RetryAfter) Error() string { return fmt.Sprintf("telegram: подождать %s", e.Wait) }

type apiResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// call вызывает метод Bot API. Токен сидит прямо в адресе запроса, а
// сетевые ошибки Go печатают адрес целиком — поэтому токен вырезается
// из любой ошибки, прежде чем она попадёт в лог.
func (t *Telegram) call(ctx context.Context, method string, payload any, out any) error {
	body, _ := json.Marshal(payload)
	base := t.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+t.Token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return t.hide(err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 70 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return t.hide(err)
	}
	defer resp.Body.Close()
	var r apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d, ответ не JSON", method, resp.StatusCode)
	}
	if !r.OK {
		if r.Parameters.RetryAfter > 0 {
			return RetryAfter{Wait: time.Duration(r.Parameters.RetryAfter) * time.Second}
		}
		return fmt.Errorf("telegram %s: %s", method, r.Description)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

func (t *Telegram) hide(err error) error {
	if t.Token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), t.Token, "***"))
}

// Send отправляет сообщение в разметке HTML.
func (t *Telegram) Send(ctx context.Context, chatID, html string) error {
	return t.call(ctx, "sendMessage", map[string]any{
		"chat_id":                  chatID,
		"text":                     html,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}, nil)
}

// Username — имя бота (@snag_alerts_bot), чтобы подсказать его в интерфейсе.
func (t *Telegram) Username(ctx context.Context) (string, error) {
	var me struct {
		Username string `json:"username"`
	}
	err := t.call(ctx, "getMe", map[string]any{}, &me)
	return me.Username, err
}

// Poll отвечает на /start и /chatid номером чата: так пользователь узнаёт,
// что вписать в настройки проекта. onStart (может быть nil) узнаёт о
// каждом таком чате — для лога. Работает, пока жив контекст.
func (t *Telegram) Poll(ctx context.Context, onStart func(chatID string), onError func(error)) {
	offset := 0
	for ctx.Err() == nil {
		var updates []struct {
			UpdateID int `json:"update_id"`
			Message  *struct {
				Text string `json:"text"`
				Chat struct {
					ID    int64  `json:"id"`
					Type  string `json:"type"`
					Title string `json:"title"`
				} `json:"chat"`
			} `json:"message"`
		}
		err := t.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 50, "allowed_updates": []string{"message"}}, &updates)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			onError(err)
			wait := 5 * time.Second
			var ra RetryAfter
			if errors.As(err, &ra) {
				wait = ra.Wait
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			if u.Message == nil {
				continue
			}
			cmd, _, _ := strings.Cut(strings.TrimSpace(u.Message.Text), " ")
			cmd, _, _ = strings.Cut(cmd, "@") // /chatid@snag_bot в группах
			if cmd != "/start" && cmd != "/chatid" {
				continue
			}
			id := strconv.FormatInt(u.Message.Chat.ID, 10)
			if onStart != nil {
				onStart(id)
			}
			text := "Номер этого чата: <code>" + id + "</code>\n\nВставьте его в Snag: проект → «Уведомления» → «Добавить чат»."
			if err := t.Send(ctx, id, text); err != nil {
				onError(err)
			}
		}
	}
}
