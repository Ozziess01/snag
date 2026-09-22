package store

import (
	"errors"
	"regexp"
	"time"
)

// Channel — куда и о чём писать по проекту. Пока только Telegram.
type Channel struct {
	ID        uint64 `json:"id"`
	ProjectID uint64 `json:"project_id"`
	Kind      string `json:"kind"`
	// Target — chat_id: число (отрицательное у групп) или @имя канала.
	Target       string `json:"target"`
	OnNew        bool   `json:"on_new"`
	OnRegression bool   `json:"on_regression"`
	// SpikeThreshold событий за SpikeWindow минут — «всплеск». 0 — не следить.
	SpikeThreshold int       `json:"spike_threshold"`
	SpikeWindow    int       `json:"spike_window"`
	CreatedAt      time.Time `json:"created_at"`
}

const KindTelegram = "telegram"

// ValidationError — настройки заполнены неверно; текст можно показать
// пользователю как есть.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

var (
	reChatID = regexp.MustCompile(`^(-?\d{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)

	ErrDuplicate = errors.New("store: этот чат уже подключён к проекту")
)

// Validate проверяет и дополняет настройки канала значениями по умолчанию.
func (c *Channel) Validate() error {
	if c.Kind == "" {
		c.Kind = KindTelegram
	}
	if c.Kind != KindTelegram || !reChatID.MatchString(c.Target) {
		return ValidationError{"chat id — число (у групп со знаком минус) или @имя канала"}
	}
	if c.SpikeThreshold < 0 || c.SpikeThreshold > 1_000_000 {
		return ValidationError{"порог всплеска — от 0 до 1 000 000 событий"}
	}
	if c.SpikeWindow <= 0 {
		c.SpikeWindow = 5
	}
	if c.SpikeWindow > 60 {
		return ValidationError{"окно всплеска — не больше 60 минут"}
	}
	return nil
}
