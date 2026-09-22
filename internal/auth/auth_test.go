package auth

import (
	"net/http/httptest"
	"testing"
)

func TestParseHeader(t *testing.T) {
	tests := []struct {
		in   string
		key  string
		ok   bool
		ver  string
		name string
	}{
		{name: "обычный", in: "Sentry sentry_version=7, sentry_client=sentry.php/4.10.0, sentry_key=abc123", key: "abc123", ver: "7", ok: true},
		{name: "без пробелов и с кавычками", in: `Sentry sentry_key="k",sentry_version=7`, key: "k", ver: "7", ok: true},
		{name: "другой регистр схемы", in: "sentry sentry_key=k", key: "k", ok: true},
		{name: "со старым secret", in: "Sentry sentry_key=k, sentry_secret=s", key: "k", ok: true},
		{name: "чужая схема", in: "Bearer token", ok: false},
		{name: "без ключа", in: "Sentry sentry_version=7", ok: false},
		{name: "пусто", in: "", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, ok := ParseHeader(tt.in)
			if ok != tt.ok || c.Key != tt.key || (tt.ver != "" && c.Version != tt.ver) {
				t.Fatalf("получили %+v ok=%v", c, ok)
			}
		})
	}
}

func TestFromRequest(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/1/envelope/?sentry_key=fromquery&sentry_version=7&sentry_client=sentry.javascript.browser%2F9.0.0", nil)
	c, ok := FromRequest(r)
	if !ok || c.Key != "fromquery" || c.Client != "sentry.javascript.browser/9.0.0" {
		t.Fatalf("query: %+v %v", c, ok)
	}

	r.Header.Set("X-Sentry-Auth", "Sentry sentry_key=fromheader")
	if c, _ := FromRequest(r); c.Key != "fromheader" {
		t.Fatalf("заголовок должен быть важнее query, получили %q", c.Key)
	}

	r = httptest.NewRequest("POST", "/api/1/store/", nil)
	r.Header.Set("Authorization", "Sentry sentry_key=legacy")
	if c, _ := FromRequest(r); c.Key != "legacy" {
		t.Fatalf("Authorization: %q", c.Key)
	}

	if _, ok := FromRequest(httptest.NewRequest("POST", "/api/1/store/", nil)); ok {
		t.Fatal("без ключа должно быть ok=false")
	}
}

func TestParseDSN(t *testing.T) {
	tests := []struct {
		dsn, key, project string
		ok                bool
	}{
		{"https://abc@o123.ingest.sentry.io/456", "abc", "456", true},
		{"http://abc:secret@localhost:8000/7", "abc", "7", true},
		{"https://abc@example.com/sentry/prefix/9", "abc", "9", true},
		{"https://example.com/9", "", "", false},
		{"https://abc@example.com/", "", "", false},
		{"::nope", "", "", false},
	}
	for _, tt := range tests {
		key, project, ok := ParseDSN(tt.dsn)
		if key != tt.key || project != tt.project || ok != tt.ok {
			t.Errorf("%s: получили %q %q %v", tt.dsn, key, project, ok)
		}
	}
}
