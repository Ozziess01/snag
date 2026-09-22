package event

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScrub(t *testing.T) {
	in := `{
		"message": "Order 17 failed",
		"request": {
			"url": "https://shop.example/login",
			"headers": [["Authorization", "Bearer abc.def"], ["Cookie", "sid=1"], ["User-Agent", "Mozilla/5.0"]],
			"data": {"email": "bob@example.com", "password": "hunter2", "profile": {"api_key": "k-1", "age": 30}}
		},
		"extra": {"session_id": 12345, "note": "Bearer xyz", "card": "4111 1111 1111 1111", "count": 3},
		"tags": {"release": "1.0"}
	}`
	var got map[string]any
	if err := json.Unmarshal(Scrub([]byte(in)), &got); err != nil {
		t.Fatal(err)
	}
	s := func(path ...string) any {
		var cur any = got
		for _, p := range path {
			cur = cur.(map[string]any)[p]
		}
		return cur
	}
	req := s("request").(map[string]any)
	headers := req["headers"].([]any)
	if headers[0].([]any)[1] != Filtered || headers[1].([]any)[1] != Filtered || headers[2].([]any)[1] != "Mozilla/5.0" {
		t.Errorf("заголовки: %v", headers)
	}
	data := req["data"].(map[string]any)
	if data["password"] != Filtered || data["email"] != "bob@example.com" {
		t.Errorf("данные формы: %v", data)
	}
	if prof := data["profile"].(map[string]any); prof["api_key"] != Filtered || prof["age"] != float64(30) {
		t.Errorf("вложенный объект: %v", prof)
	}
	extra := s("extra").(map[string]any)
	for _, k := range []string{"session_id", "note", "card"} {
		if extra[k] != Filtered {
			t.Errorf("extra.%s = %v", k, extra[k])
		}
	}
	if extra["count"] != float64(3) || s("message") != "Order 17 failed" || s("tags", "release") != "1.0" {
		t.Errorf("лишнее вычищено: %v", got)
	}
}

func TestScrubKeepsBrokenJSON(t *testing.T) {
	if string(Scrub([]byte("{broken"))) != "{broken" {
		t.Fatal("битый JSON должен вернуться как есть")
	}
	// Большие числа не превращаются в 1e+21.
	if out := string(Scrub([]byte(`{"id": 123456789012345678901}`))); !strings.Contains(out, "123456789012345678901") {
		t.Fatalf("число испорчено: %s", out)
	}
}
