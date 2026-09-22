// Package event — событие Sentry в том виде, в каком его присылают SDK.
//
// Протокол складывался годами, и одно и то же поле у разных SDK и версий
// приходит в разной форме: exception — объект {values:[…]} или сразу массив,
// tags — объект или массив пар, timestamp — число секунд или строка RFC 3339,
// user.id — строка или число. Типы ниже принимают все эти варианты, чтобы
// остальной код работал с одной формой. Если поле не подходит ни под один
// вариант, оно остаётся пустым, а событие принимается: терять отчёт об
// ошибке из-за одного кривого поля хуже.
// Схема: https://develop.sentry.dev/sdk/data-model/event-payloads/
package event

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

type Event struct {
	EventID     string               `json:"event_id"`
	Timestamp   Time                 `json:"timestamp"`
	Level       string               `json:"level"`
	Platform    string               `json:"platform"`
	Logger      string               `json:"logger"`
	Transaction string               `json:"transaction"`
	ServerName  string               `json:"server_name"`
	Release     string               `json:"release"`
	Dist        string               `json:"dist"`
	Environment string               `json:"environment"`
	Message     Message              `json:"message"`
	LogEntry    *Message             `json:"logentry"`
	Exception   ValuesOf[Exception]  `json:"exception"`
	Breadcrumbs ValuesOf[Breadcrumb] `json:"breadcrumbs"`
	// Стектрейс бывает и у событий без исключения: captureMessage
	// с attach_stacktrace кладёт его в threads или прямо в корень.
	Threads     ValuesOf[Thread]           `json:"threads"`
	Stacktrace  *Stacktrace                `json:"stacktrace"`
	Tags        Pairs                      `json:"tags"`
	User        *User                      `json:"user"`
	Request     *Request                   `json:"request"`
	Contexts    map[string]json.RawMessage `json:"contexts"`
	Extra       map[string]json.RawMessage `json:"extra"`
	Fingerprint []string                   `json:"fingerprint"`
	SDK         *SDK                       `json:"sdk"`
}

type Exception struct {
	Type       string      `json:"type"`
	Value      string      `json:"value"`
	Module     string      `json:"module"`
	Mechanism  *Mechanism  `json:"mechanism"`
	Stacktrace *Stacktrace `json:"stacktrace"`
}

type Thread struct {
	ID         FlexString  `json:"id"`
	Name       string      `json:"name"`
	Crashed    bool        `json:"crashed"`
	Current    bool        `json:"current"`
	Stacktrace *Stacktrace `json:"stacktrace"`
}

type Mechanism struct {
	Type    string `json:"type"`
	Handled *bool  `json:"handled"`
}

type Stacktrace struct {
	Frames []Frame `json:"frames"`
}

// Frame — кадр стека. Порядок кадров в протоколе: от самого старого вызова
// к месту ошибки, то есть «виновник» — последний кадр.
type Frame struct {
	Filename    string   `json:"filename"`
	AbsPath     string   `json:"abs_path"`
	Function    string   `json:"function"`
	Module      string   `json:"module"`
	Package     string   `json:"package"`
	Lineno      int      `json:"lineno"`
	Colno       int      `json:"colno"`
	InApp       *bool    `json:"in_app"`
	ContextLine string   `json:"context_line"`
	PreContext  []string `json:"pre_context"`
	PostContext []string `json:"post_context"`
}

type Breadcrumb struct {
	Timestamp Time                       `json:"timestamp"`
	Type      string                     `json:"type"`
	Category  string                     `json:"category"`
	Message   string                     `json:"message"`
	Level     string                     `json:"level"`
	Data      map[string]json.RawMessage `json:"data"`
}

type User struct {
	ID        FlexString `json:"id"`
	Email     string     `json:"email"`
	Username  string     `json:"username"`
	IPAddress string     `json:"ip_address"`
}

type Request struct {
	URL         string          `json:"url"`
	Method      string          `json:"method"`
	QueryString json.RawMessage `json:"query_string"`
	Headers     Pairs           `json:"headers"`
	Data        json.RawMessage `json:"data"`
	Cookies     json.RawMessage `json:"cookies"`
	Env         json.RawMessage `json:"env"`
}

type SDK struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ---------- гибкие типы ----------

// Time принимает число секунд Unix (с дробной частью) или строку RFC 3339.
// Строку без часового пояса протокол трактует как UTC.
type Time struct{ time.Time }

func (t *Time) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == `""` {
		return nil
	}
	if s[0] != '"' {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil // непонятное время заменит Normalize
		}
		sec := int64(f)
		t.Time = time.Unix(sec, int64((f-float64(sec))*1e9)).UTC()
		return nil
	}
	str, err := strconv.Unquote(s)
	if err != nil {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999"} {
		if v, err := time.Parse(layout, str); err == nil {
			t.Time = v.UTC()
			return nil
		}
	}
	return nil
}

func (t Time) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(t.UTC().Format(time.RFC3339Nano))
}

// FlexString принимает строку или число (user.id бывает и тем и другим).
type FlexString string

func (s *FlexString) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		*s = FlexString(str)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*s = FlexString(n.String())
		return nil
	}
	return nil
}

// Message: строка или объект {message, formatted, params}. Так же выглядит
// logentry, поэтому тип общий.
type Message struct {
	Message   string          `json:"message"`
	Formatted string          `json:"formatted"`
	Params    json.RawMessage `json:"params"`
}

func (m *Message) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		m.Formatted = s
		return nil
	}
	type plain Message
	_ = json.Unmarshal(b, (*plain)(m))
	return nil
}

// Text — текст для показа: подставленный вариант, если есть.
func (m Message) Text() string {
	if m.Formatted != "" {
		return m.Formatted
	}
	return m.Message
}

// Template — шаблон до подстановки параметров: для группировки он лучше,
// «User 5 not found» и «User 7 not found» — одна проблема.
func (m Message) Template() string {
	if m.Message != "" {
		return m.Message
	}
	return m.Formatted
}

// ValuesOf принимает {"values":[…]} и просто […].
type ValuesOf[T any] []T

func (v *ValuesOf[T]) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '[' {
		var list []T
		if lenient(json.Unmarshal(b, &list)) {
			*v = list
		}
		return nil
	}
	var obj struct {
		Values []T `json:"values"`
	}
	if lenient(json.Unmarshal(b, &obj)) {
		*v = obj.Values
	}
	return nil
}

// lenient: ошибка типа в одном вложенном поле не повод выбрасывать весь
// список — json.Unmarshal в этом случае заполняет всё остальное.
func lenient(err error) bool {
	var typeErr *json.UnmarshalTypeError
	return err == nil || errors.As(err, &typeErr)
}

func (v ValuesOf[T]) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Values []T `json:"values"`
	}{v})
}

// Pairs — упорядоченные пары ключ-значение. Принимает объект
// {"k":"v"} или массив [["k","v"]]; нестроковые значения превращаются
// в строку.
type Pairs [][2]string

func (p *Pairs) UnmarshalJSON(b []byte) error {
	var list [][]json.RawMessage
	if err := json.Unmarshal(b, &list); err == nil {
		out := make(Pairs, 0, len(list))
		for _, kv := range list {
			if len(kv) == 2 {
				out = append(out, [2]string{rawString(kv[0]), rawString(kv[1])})
			}
		}
		*p = out
		return nil
	}
	// Объект разбираем токенами, чтобы сохранить порядок ключей.
	dec := json.NewDecoder(strings.NewReader(string(b)))
	tok, err := dec.Token()
	if d, ok := tok.(json.Delim); err != nil || !ok || d != '{' {
		return nil
	}
	out := Pairs{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			break
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			break
		}
		out = append(out, [2]string{kt.(string), rawString(raw)})
	}
	*p = out
	return nil
}

func (p Pairs) Get(key string) (string, bool) {
	for _, kv := range p {
		if kv[0] == key {
			return kv[1], true
		}
	}
	return "", false
}

func rawString(r json.RawMessage) string {
	var s string
	if err := json.Unmarshal(r, &s); err == nil {
		return s
	}
	if string(r) == "null" {
		return ""
	}
	return string(r)
}
