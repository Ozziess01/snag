package envelope

import (
	"errors"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		types []string
		body  []string
	}{
		{
			name:  "элементы без length",
			in:    "{\"event_id\":\"9ec79c33ec9942ab8353589fcb2e04dc\"}\n{\"type\":\"event\"}\n{\"message\":\"hi\"}\n",
			types: []string{"event"},
			body:  []string{`{"message":"hi"}`},
		},
		{
			name:  "length с переводами строк внутри тела",
			in:    "{}\n{\"type\":\"attachment\",\"length\":7}\nab\ncd\ne\n{\"type\":\"event\",\"length\":2}\n{}",
			types: []string{"attachment", "event"},
			body:  []string{"ab\ncd\ne", "{}"},
		},
		{
			name:  "последний элемент без перевода строки",
			in:    "{}\n{\"type\":\"event\"}\n{\"a\":1}",
			types: []string{"event"},
			body:  []string{`{"a":1}`},
		},
		{
			name:  "CRLF и пустые строки между элементами",
			in:    "{}\r\n\r\n{\"type\":\"session\"}\r\n{\"sid\":\"x\"}\r\n\n{\"type\":\"event\",\"length\":0}\n\n",
			types: []string{"session", "event"},
			body:  []string{`{"sid":"x"}`, ""},
		},
		{
			name:  "только заголовок",
			in:    "{\"dsn\":\"https://k@h/1\"}\n",
			types: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := Parse([]byte(tt.in), Limits{})
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(env.Items) != len(tt.types) {
				t.Fatalf("элементов %d, ждали %d", len(env.Items), len(tt.types))
			}
			for i, it := range env.Items {
				if it.Header.Type != tt.types[i] {
					t.Errorf("элемент %d: type %q, ждали %q", i, it.Header.Type, tt.types[i])
				}
				if tt.body != nil && string(it.Payload) != tt.body[i] {
					t.Errorf("элемент %d: тело %q, ждали %q", i, it.Payload, tt.body[i])
				}
			}
		})
	}
}

func TestParseHeader(t *testing.T) {
	env, err := Parse([]byte(`{"event_id":"abc","dsn":"https://key@o1.ingest/5","sdk":{"name":"sentry.go","version":"0.30.0"}}`+"\n"), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if env.Header.EventID != "abc" || env.Header.DSN != "https://key@o1.ingest/5" || env.Header.SDK.Name != "sentry.go" {
		t.Fatalf("заголовок разобран неверно: %+v", env.Header)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		lim  Limits
		want error
	}{
		{"пусто", "", Limits{}, ErrEmpty},
		{"пробелы", "  \n", Limits{}, ErrEmpty},
		{"заголовок не объект", "[1,2]\n", Limits{}, ErrBadHeader},
		{"заголовок битый", "{nope\n", Limits{}, ErrBadHeader},
		{"элемент без type", "{}\n{\"length\":1}\nx", Limits{}, ErrBadItem},
		{"элемент не JSON", "{}\nhello\n", Limits{}, ErrBadItem},
		{"length больше данных", "{}\n{\"type\":\"event\",\"length\":100}\n{}", Limits{}, ErrTruncated},
		{"отрицательный length", "{}\n{\"type\":\"event\",\"length\":-1}\n{}", Limits{}, ErrTruncated},
		{"лимит элементов", "{}\n{\"type\":\"a\"}\n{}\n{\"type\":\"b\"}\n{}\n", Limits{MaxItems: 1}, ErrTooManyItems},
		{"лимит размера", "{}\n{\"type\":\"event\"}\n{\"message\":\"long\"}\n", Limits{MaxItemSize: 5}, ErrItemTooBig},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in), tt.lim)
			if !errors.Is(err, tt.want) {
				t.Fatalf("ошибка %v, ждали %v", err, tt.want)
			}
		})
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("{}\n{\"type\":\"event\"}\n{}\n"))
	f.Add([]byte("{}\n{\"type\":\"attachment\",\"length\":3}\nabc"))
	f.Fuzz(func(t *testing.T, data []byte) {
		env, err := Parse(data, Limits{MaxItems: 100})
		if err != nil {
			return
		}
		for _, it := range env.Items {
			if it.Header.Length != nil && len(it.Payload) != *it.Header.Length {
				t.Fatalf("длина тела %d не совпадает с length %d", len(it.Payload), *it.Header.Length)
			}
		}
	})
}
