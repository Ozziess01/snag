package grouping

import (
	"strings"
	"testing"

	"github.com/Ozziess01/snag/internal/event"
)

var yes, no = true, false

func exc(typ, value string, frames ...event.Frame) *event.Event {
	return &event.Event{Exception: event.ValuesOf[event.Exception]{{
		Type: typ, Value: value, Stacktrace: &event.Stacktrace{Frames: frames},
	}}}
}

func fr(file, fn string, line int) event.Frame {
	return event.Frame{Filename: file, Function: fn, Lineno: line, InApp: &yes}
}

func same(t *testing.T, a, b *event.Event) {
	t.Helper()
	ra, rb := Compute(a), Compute(b)
	if ra.Hash != rb.Hash {
		t.Fatalf("ждали одну группу:\n  %v\n  %v", ra.Parts, rb.Parts)
	}
}

func differ(t *testing.T, a, b *event.Event) {
	t.Helper()
	ra, rb := Compute(a), Compute(b)
	if ra.Hash == rb.Hash {
		t.Fatalf("ждали разные группы, а склеилось по %v", ra.Parts)
	}
}

func TestStacktraceGrouping(t *testing.T) {
	base := exc("TypeError", "x is undefined", fr("cart.js", "render", 10), fr("cart.js", "total", 20))

	t.Run("другие номера строк — та же проблема", func(t *testing.T) {
		same(t, base, exc("TypeError", "x is undefined", fr("cart.js", "render", 14), fr("cart.js", "total", 31)))
	})
	t.Run("другой текст при том же стеке — та же проблема", func(t *testing.T) {
		same(t, exc("NotFound", "User 5 not found", fr("u.php", "find", 1)), exc("NotFound", "User 7 not found", fr("u.php", "find", 1)))
	})
	t.Run("другая функция — другая проблема", func(t *testing.T) {
		differ(t, base, exc("TypeError", "x is undefined", fr("cart.js", "render", 10), fr("cart.js", "subtotal", 20)))
	})
	t.Run("другой тип — другая проблема", func(t *testing.T) {
		differ(t, base, exc("RangeError", "x is undefined", fr("cart.js", "render", 10), fr("cart.js", "total", 20)))
	})
	t.Run("кадры фреймворка не влияют, если in_app размечен", func(t *testing.T) {
		withFramework := exc("TypeError", "x", fr("cart.js", "render", 10), fr("cart.js", "total", 20),
			event.Frame{Filename: "node_modules/react-dom.js", Function: "commitRoot", InApp: &no})
		same(t, base, withFramework)
	})
	t.Run("глубина рекурсии не влияет", func(t *testing.T) {
		shallow := exc("RangeError", "stack", fr("tree.js", "walk", 5), fr("tree.js", "walk", 5), fr("tree.js", "visit", 9))
		deep := exc("RangeError", "stack", fr("tree.js", "walk", 5), fr("tree.js", "walk", 5), fr("tree.js", "walk", 5), fr("tree.js", "walk", 5), fr("tree.js", "visit", 9))
		same(t, shallow, deep)
	})
	t.Run("хеш в имени бандла и хост не влияют", func(t *testing.T) {
		a := exc("TypeError", "x", fr("https://shop.example/assets/app.3f9a1c7b.js?v=1", "render", 1))
		b := exc("TypeError", "x", fr("https://cdn.shop.example/assets/app.8b2e11d0.js", "render", 1))
		same(t, a, b)
	})
	t.Run("модуль важнее абсолютного пути", func(t *testing.T) {
		a := exc("E", "", event.Frame{Module: "app.orders", Filename: "/srv/releases/20260901/app/orders.py", Function: "pay", InApp: &yes})
		b := exc("E", "", event.Frame{Module: "app.orders", Filename: "/srv/releases/20260915/app/orders.py", Function: "pay", InApp: &yes})
		same(t, a, b)
	})
	t.Run("номера замыканий Go не влияют", func(t *testing.T) {
		same(t, exc("panic", "", fr("main.go", "main.func2", 1)), exc("panic", "", fr("main.go", "main.func3", 1)))
	})
	t.Run("безымянная функция: берём строку кода, а не номер строки", func(t *testing.T) {
		a := exc("TypeError", "", event.Frame{Filename: "a.js", Function: "?", Lineno: 10, ContextLine: "  cart.items.length", InApp: &yes})
		b := exc("TypeError", "", event.Frame{Filename: "a.js", Function: "<anonymous>", Lineno: 42, ContextLine: "cart.items.length  ", InApp: &yes})
		c := exc("TypeError", "", event.Frame{Filename: "a.js", Function: "?", Lineno: 10, ContextLine: "user.name.trim()", InApp: &yes})
		same(t, a, b)
		differ(t, a, c)
	})
}

func TestChainedExceptions(t *testing.T) {
	a := &event.Event{Exception: event.ValuesOf[event.Exception]{
		{Type: "KeyError", Stacktrace: &event.Stacktrace{Frames: []event.Frame{fr("cfg.py", "get", 1)}}},
		{Type: "RuntimeError", Stacktrace: &event.Stacktrace{Frames: []event.Frame{fr("app.py", "load", 1)}}},
	}}
	b := &event.Event{Exception: event.ValuesOf[event.Exception]{
		{Type: "OSError", Stacktrace: &event.Stacktrace{Frames: []event.Frame{fr("cfg.py", "read", 1)}}},
		{Type: "RuntimeError", Stacktrace: &event.Stacktrace{Frames: []event.Frame{fr("app.py", "load", 1)}}},
	}}
	differ(t, a, b) // обёртка одна, но причины разные
}

func TestExceptionWithoutStack(t *testing.T) {
	same(t, exc("Timeout", "request 8f2c1a9e0b took 3012 ms"), exc("Timeout", "request 11aa22bb33 took 45 ms"))
	differ(t, exc("Timeout", "database timeout"), exc("Timeout", "redis timeout"))
}

func TestMessages(t *testing.T) {
	tpl := func(msg, formatted string) *event.Event {
		return &event.Event{LogEntry: &event.Message{Message: msg, Formatted: formatted}}
	}
	same(t, tpl("Order %s failed", "Order 17 failed"), tpl("Order %s failed", "Order 18 failed"))
	same(t, &event.Event{Message: event.Message{Formatted: "Order 17 failed"}}, &event.Event{Message: event.Message{Formatted: "Order 18 failed"}})
	differ(t, tpl("Order %s failed", ""), tpl("Payment %s failed", ""))
	differ(t, &event.Event{Message: event.Message{Formatted: "x"}, Logger: "billing"}, &event.Event{Message: event.Message{Formatted: "x"}, Logger: "shop"})
}

func TestCustomFingerprint(t *testing.T) {
	a := exc("E", "", fr("a.go", "f", 1))
	a.Fingerprint = []string{"payment-gateway-down"}
	b := exc("Other", "", fr("b.go", "g", 1))
	b.Fingerprint = []string{"payment-gateway-down"}
	same(t, a, b)
	if Compute(a).Kind != "custom" {
		t.Fatal("kind")
	}

	// {{ default }} + уточнение: делим стандартную группу по клиенту.
	c := exc("E", "", fr("a.go", "f", 1))
	c.Fingerprint = []string{"{{ default }}", "tenant-1"}
	d := exc("E", "", fr("a.go", "f", 1))
	d.Fingerprint = []string{"{{default}}", "tenant-2"}
	differ(t, c, d)
	if parts := Compute(c).Parts; parts[len(parts)-1] != "tenant-1" || !strings.HasPrefix(parts[0], "type:") {
		t.Fatalf("parts %v", parts)
	}
}

func TestParameterize(t *testing.T) {
	tests := map[string]string{
		"Order 17 failed":                                "Order <int> failed",
		"User bob@example.com not found":                 "User <email> not found",
		"GET https://api.example/v1/items?id=5 → 502":    "GET <url> → <int>",
		"id 9ec79c33-ec99-42ab-8353-589fcb2e04dc":        "id <uuid>",
		"connect 10.0.0.12:5432 refused":                 "connect <ip> refused",
		"at 2026-09-23T10:00:00Z":                        "at <date>",
		"ptr 0xc000123abc":                               "ptr <hex>",
		"token a3f9c01b7e2d missing":                     "token <hex> missing",
		"Cannot read properties of null (reading 'map')": "Cannot read properties of null (reading 'map')",
		"invalid literal for int() with base 10: 'три'":  "invalid literal for int() with base <int>: 'три'",
	}
	for in, want := range tests {
		if got := Parameterize(in); got != want {
			t.Errorf("%q\n  получили %q\n  ждали    %q", in, got, want)
		}
	}
}

func TestHashIsStable(t *testing.T) {
	// Хеш уходит в базу, поэтому не должен меняться от запуска к запуску.
	r := Compute(exc("TypeError", "x", fr("cart.js", "render", 10)))
	if r.Hex() != Compute(exc("TypeError", "y", fr("cart.js", "render", 99))).Hex() || len(r.Hex()) != 16 {
		t.Fatalf("hex %s", r.Hex())
	}
}
