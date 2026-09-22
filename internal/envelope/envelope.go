// Package envelope разбирает конверты Sentry: формат, в котором SDK
// отправляют события на /api/<project>/envelope/.
//
// Конверт не JSON, а построчный формат:
//
//	{"event_id":"…","dsn":"…"}          ← заголовок конверта, одна строка JSON
//	{"type":"event","length":41}        ← заголовок элемента
//	{"message":"hello","level":"error"} ← тело элемента
//	{"type":"attachment","length":4}
//	\xde\xad\xbe\xef
//
// Если в заголовке элемента есть length, тело читается ровно такой длины:
// внутри могут быть переводы строк и бинарные данные. Если length нет,
// тело идёт до ближайшего \n. Описание формата:
// https://develop.sentry.dev/sdk/envelopes/
package envelope

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Типы элементов, которые встречаются чаще всего.
const (
	TypeEvent        = "event"
	TypeTransaction  = "transaction"
	TypeSession      = "session"
	TypeSessions     = "sessions"
	TypeAttachment   = "attachment"
	TypeClientReport = "client_report"
)

var (
	ErrEmpty        = errors.New("envelope: пустой конверт")
	ErrBadHeader    = errors.New("envelope: заголовок конверта не JSON-объект")
	ErrBadItem      = errors.New("envelope: заголовок элемента не JSON-объект или без type")
	ErrTruncated    = errors.New("envelope: length больше оставшихся данных")
	ErrTooManyItems = errors.New("envelope: слишком много элементов")
	ErrItemTooBig   = errors.New("envelope: элемент больше допустимого размера")
)

// Header — первая строка конверта.
type Header struct {
	EventID string   `json:"event_id,omitempty"`
	DSN     string   `json:"dsn,omitempty"`
	SentAt  string   `json:"sent_at,omitempty"`
	SDK     *SDKInfo `json:"sdk,omitempty"`
}

type SDKInfo struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// ItemHeader — строка перед телом каждого элемента.
type ItemHeader struct {
	Type        string `json:"type"`
	Length      *int   `json:"length,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Filename    string `json:"filename,omitempty"`
}

type Item struct {
	Header  ItemHeader
	Payload []byte
}

type Envelope struct {
	Header Header
	Items  []Item
}

// Limits ограничивает разбор. Нулевое значение поля — без ограничения.
type Limits struct {
	MaxItems    int
	MaxItemSize int
}

// Parse разбирает конверт целиком. Тело элемента — срез исходного data,
// без копирования.
func Parse(data []byte, lim Limits) (*Envelope, error) {
	line, rest := cutLine(data)
	if len(bytes.TrimSpace(line)) == 0 {
		return nil, ErrEmpty
	}
	env := &Envelope{}
	if !isObject(line) || json.Unmarshal(line, &env.Header) != nil {
		return nil, ErrBadHeader
	}

	for len(rest) > 0 {
		hline, after := cutLine(rest)
		// Пустые строки между элементами встречаются у старых SDK, пропускаем.
		if len(bytes.TrimSpace(hline)) == 0 {
			rest = after
			continue
		}
		var ih ItemHeader
		if !isObject(hline) || json.Unmarshal(hline, &ih) != nil || ih.Type == "" {
			return nil, fmt.Errorf("%w (элемент %d)", ErrBadItem, len(env.Items)+1)
		}

		var payload []byte
		if ih.Length != nil {
			n := *ih.Length
			if n < 0 || n > len(after) {
				return nil, fmt.Errorf("%w (элемент %d, length=%d, осталось %d)", ErrTruncated, len(env.Items)+1, n, len(after))
			}
			payload, rest = after[:n], after[n:]
			rest = trimNewline(rest)
		} else {
			payload, rest = cutLine(after)
		}

		if lim.MaxItemSize > 0 && len(payload) > lim.MaxItemSize {
			return nil, fmt.Errorf("%w (%s, %d байт)", ErrItemTooBig, ih.Type, len(payload))
		}
		env.Items = append(env.Items, Item{Header: ih, Payload: payload})
		if lim.MaxItems > 0 && len(env.Items) > lim.MaxItems {
			return nil, ErrTooManyItems
		}
	}
	return env, nil
}

// cutLine отрезает строку до \n (\r в конце тоже убирается).
func cutLine(b []byte) (line, rest []byte) {
	i := bytes.IndexByte(b, '\n')
	if i < 0 {
		return bytes.TrimSuffix(b, []byte{'\r'}), nil
	}
	return bytes.TrimSuffix(b[:i], []byte{'\r'}), b[i+1:]
}

// trimNewline убирает один перевод строки после тела с явной длиной.
func trimNewline(b []byte) []byte {
	if bytes.HasPrefix(b, []byte("\r\n")) {
		return b[2:]
	}
	if len(b) > 0 && b[0] == '\n' {
		return b[1:]
	}
	return b
}

func isObject(b []byte) bool {
	b = bytes.TrimSpace(b)
	return len(b) >= 2 && b[0] == '{' && b[len(b)-1] == '}'
}
