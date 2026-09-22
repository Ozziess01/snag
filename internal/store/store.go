// Package store — общие типы хранилищ. Реализации: pg (проекты,
// пользователи, проблемы), ch (события) и mem (всё в памяти — для тестов
// и для демо, которое работает прямо в браузере).
//
// Типы вынесены сюда, чтобы код, который работает с данными (конвейер,
// API), не тянул за собой драйверы баз: в браузерной сборке их нет.
package store

import (
	"errors"
	"time"
)

var ErrNotFound = errors.New("store: не найдено")

// Статусы проблемы.
const (
	StatusUnresolved = "unresolved"
	StatusResolved   = "resolved"
	StatusIgnored    = "ignored"
)

func ValidStatus(s string) bool {
	return s == StatusUnresolved || s == StatusResolved || s == StatusIgnored
}

// ---------- запись ----------

// IssueDelta — что пачка событий меняет в одной проблеме.
type IssueDelta struct {
	ProjectID   uint64
	Fingerprint uint64
	Kind        string
	Title       string
	Culprit     string
	Level       string
	Platform    string
	FirstSeen   time.Time
	LastSeen    time.Time
	Count       int64
}

type IssueResult struct {
	ID          uint64
	ProjectID   uint64
	Fingerprint uint64
	Created     bool // проблема появилась впервые
	Regressed   bool // была решена, но случилась снова
}

// Event — событие в хранилище.
type Event struct {
	ProjectID   uint64
	IssueID     uint64
	EventID     string // 32 hex
	Timestamp   time.Time
	ReceivedAt  time.Time
	Level       string
	Platform    string
	Environment string
	Release     string
	SDK         string
	Title       string
	Culprit     string
	UserKey     string
	Tags        map[string]string
	Data        string // исходный JSON события
}

// ---------- чтение ----------

type Project struct {
	ID             uint64    `json:"id"`
	Name           string    `json:"name"`
	Slug           string    `json:"slug"`
	PublicKey      string    `json:"public_key"`
	AllowedOrigins []string  `json:"allowed_origins"`
	CreatedAt      time.Time `json:"created_at"`
}

type Issue struct {
	ID          uint64     `json:"id"`
	ProjectID   uint64     `json:"project_id"`
	Kind        string     `json:"grouping"`
	Title       string     `json:"title"`
	Culprit     string     `json:"culprit"`
	Level       string     `json:"level"`
	Platform    string     `json:"platform"`
	Status      string     `json:"status"`
	FirstSeen   time.Time  `json:"first_seen"`
	LastSeen    time.Time  `json:"last_seen"`
	TimesSeen   int64      `json:"times_seen"`
	ResolvedAt  *time.Time `json:"resolved_at"`
	RegressedAt *time.Time `json:"regressed_at"`
}

// IssueFilter — выборка проблем проекта.
type IssueFilter struct {
	ProjectID uint64
	Status    string // пусто — все
	Limit     int
}

// Stats — счётчики проблемы за период и разбивка по интервалам для графика.
type Stats struct {
	Events  uint64   `json:"events"`
	Users   uint64   `json:"users"`
	Buckets []uint64 `json:"buckets"`
}

// Period — окно статистики: от Since до Until шагами по Step.
type Period struct {
	Since time.Time
	Until time.Time
	Step  time.Duration
}

func (p Period) Buckets() int {
	return int(p.Until.Sub(p.Since) / p.Step)
}

// TagValue — одно значение тега и сколько раз оно встречалось.
type TagValue struct {
	Value string `json:"value"`
	Count uint64 `json:"count"`
}

// EventSummary — событие в списке событий проблемы.
type EventSummary struct {
	EventID     string    `json:"id"`
	Timestamp   time.Time `json:"timestamp"`
	Level       string    `json:"level"`
	Release     string    `json:"release"`
	Environment string    `json:"environment"`
	UserKey     string    `json:"user"`
}
