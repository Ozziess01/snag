package event

import (
	"encoding/json"
	"testing"
)

func TestParseUserAgent(t *testing.T) {
	tests := []struct{ ua, browser, os string }{
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36", "Chrome 128", "Windows 10"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 YaBrowser/24.7.0.0 Safari/537.36", "Yandex Browser 24", "Windows 10"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36 Edg/128.0.2739.42", "Edge 128", "Windows 10"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15", "Safari 17", "macOS 10.15"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1", "Safari 17", "iOS 17"},
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Mobile Safari/537.36", "Chrome 128", "Android 14"},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0", "Firefox 130", "Linux"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/140.0.0.0 Safari/537.36", "HeadlessChrome 140", "Windows 10"},
		{"curl/8.4.0", "", ""},
	}
	for _, tt := range tests {
		b, o := ParseUserAgent(tt.ua)
		if b != tt.browser || o != tt.os {
			t.Errorf("%s\n  получили %q / %q, ждали %q / %q", tt.ua, b, o, tt.browser, tt.os)
		}
	}
}

func TestDerivedTags(t *testing.T) {
	var e Event
	_ = json.Unmarshal([]byte(`{
		"level": "error", "environment": "production", "release": "shop@2.1.0",
		"tags": {"browser": "явно указан"},
		"contexts": {"os": {"name": "Windows", "version": "10.0.19045"}, "runtime": {"name": "node", "version": "v24.15.0"}},
		"request": {"url": "https://shop.example/cart?id=5", "headers": [["user-agent", "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0"]]},
		"user": {"id": 7}
	}`), &e)
	tags := DerivedTags(&e)
	want := map[string]string{
		"browser":     "явно указан", // присланный тег не перезаписывается
		"os":          "Windows 10",  // из контекста, а не из User-Agent
		"runtime":     "node 24",
		"environment": "production",
		"release":     "shop@2.1.0",
		"level":       "error",
		"url":         "https://shop.example/cart",
		"user":        "id:7",
	}
	for k, v := range want {
		if tags[k] != v {
			t.Errorf("%s = %q, ждали %q", k, tags[k], v)
		}
	}
}
