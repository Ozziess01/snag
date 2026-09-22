package event

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// Ограничения примерно как у самого Sentry: событие не должно раздувать
// хранилище из-за одного огромного поля.
const (
	maxTextLen     = 8 * 1024
	maxTagKeyLen   = 200
	maxTagValueLen = 200
	maxBreadcrumbs = 100
	maxFrames      = 250
	maxFutureSkew  = time.Minute
	maxAge         = 30 * 24 * time.Hour
)

var ErrNotObject = errors.New("event: тело события не JSON-объект")

// Decode разбирает JSON события. Поле неожиданного типа (скажем, lineno
// строкой) не роняет всё событие: оно просто остаётся пустым. Лучше потерять
// одно поле, чем отчёт об ошибке целиком.
func Decode(b []byte) (*Event, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' {
		return nil, ErrNotObject
	}
	var e Event
	err := json.Unmarshal(b, &e)
	var typeErr *json.UnmarshalTypeError
	if err != nil && !errors.As(err, &typeErr) {
		return nil, err
	}
	return &e, nil
}

// Normalize приводит событие к одной форме. fallbackID — event_id из
// заголовка конверта, received — время получения на сервере.
func Normalize(e *Event, fallbackID string, received time.Time) {
	e.EventID = normalizeID(e.EventID, fallbackID)

	ts := e.Timestamp.Time
	if ts.IsZero() || ts.After(received.Add(maxFutureSkew)) || ts.Before(received.Add(-maxAge)) {
		// Часам клиента верить нельзя: у пользователя может стоять 2031 год.
		ts = received
	}
	e.Timestamp = Time{ts.UTC()}

	e.Level = normalizeLevel(e.Level)
	if e.Platform == "" {
		e.Platform = "other"
	}

	e.Message.Formatted = truncate(e.Message.Formatted, maxTextLen)
	e.Message.Message = truncate(e.Message.Message, maxTextLen)
	if e.LogEntry != nil {
		e.LogEntry.Formatted = truncate(e.LogEntry.Formatted, maxTextLen)
		e.LogEntry.Message = truncate(e.LogEntry.Message, maxTextLen)
	}
	for i := range e.Exception {
		ex := &e.Exception[i]
		ex.Value = truncate(ex.Value, maxTextLen)
		if ex.Stacktrace != nil && len(ex.Stacktrace.Frames) > maxFrames {
			// Оставляем кадры ближе к месту падения: они в конце.
			ex.Stacktrace.Frames = ex.Stacktrace.Frames[len(ex.Stacktrace.Frames)-maxFrames:]
		}
	}
	if len(e.Breadcrumbs) > maxBreadcrumbs {
		e.Breadcrumbs = e.Breadcrumbs[len(e.Breadcrumbs)-maxBreadcrumbs:]
	}
	for i := range e.Tags {
		e.Tags[i][0] = truncate(e.Tags[i][0], maxTagKeyLen)
		e.Tags[i][1] = truncate(e.Tags[i][1], maxTagValueLen)
	}
}

// Primary — главное исключение. В протоколе цепочка идёт от старого
// к новому, поэтому это последнее.
func (e *Event) Primary() *Exception {
	if len(e.Exception) == 0 {
		return nil
	}
	return &e.Exception[len(e.Exception)-1]
}

// Title — короткий заголовок для списка: «TypeError: x is undefined».
func (e *Event) Title() string {
	if ex := e.Primary(); ex != nil {
		switch {
		case ex.Type != "" && ex.Value != "":
			return firstLine(ex.Type + ": " + ex.Value)
		case ex.Type != "":
			return ex.Type
		case ex.Value != "":
			return firstLine(ex.Value)
		}
	}
	if e.LogEntry != nil && e.LogEntry.Text() != "" {
		return firstLine(e.LogEntry.Text())
	}
	if t := e.Message.Text(); t != "" {
		return firstLine(t)
	}
	return "<без названия>"
}

// Culprit — где случилась ошибка: последний кадр кода приложения.
func (e *Event) Culprit() string {
	ex := e.Primary()
	if ex == nil || ex.Stacktrace == nil {
		return e.Transaction
	}
	f := lastFrame(ex.Stacktrace.Frames)
	if f == nil {
		return e.Transaction
	}
	where := f.Filename
	if where == "" {
		where = f.Module
	}
	fn := f.Function
	if isAnonymous(fn) {
		fn = ""
	}
	switch {
	case where != "" && fn != "":
		return where + " in " + fn
	case where != "":
		return where
	default:
		return fn
	}
}

// isAnonymous: так SDK подписывают безымянные функции и код верхнего уровня.
func isAnonymous(fn string) bool {
	switch fn {
	case "?", "<anonymous>", "<unknown>", "{main}", "<module>":
		return true
	}
	return false
}

// lastFrame — последний кадр с in_app=true, а если SDK не размечал
// in_app, просто последний.
func lastFrame(frames []Frame) *Frame {
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i].InApp != nil && *frames[i].InApp {
			return &frames[i]
		}
	}
	if len(frames) == 0 {
		return nil
	}
	return &frames[len(frames)-1]
}

func normalizeID(id, fallback string) string {
	for _, v := range []string{id, fallback} {
		v = strings.ToLower(strings.ReplaceAll(v, "-", ""))
		if len(v) == 32 {
			if _, err := hex.DecodeString(v); err == nil {
				return v
			}
		}
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func normalizeLevel(l string) string {
	switch strings.ToLower(strings.TrimSpace(l)) {
	case "debug", "trace":
		return "debug"
	case "info", "log", "notice":
		return "info"
	case "warning", "warn":
		return "warning"
	case "fatal", "critical", "emergency", "alert":
		return "fatal"
	default:
		return "error"
	}
}

// truncate режет по границе символа, а не байта, чтобы не ломать UTF-8.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncate(strings.TrimSpace(s), 200)
}
