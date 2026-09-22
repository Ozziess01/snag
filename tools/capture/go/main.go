package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
)

func loadConfig(path string) error {
	if _, err := os.ReadFile(path); err != nil {
		return fmt.Errorf("load config %s: %w", path, err)
	}
	return nil
}

func main() {
	err := sentry.Init(sentry.ClientOptions{
		Dsn:              "http://gosdk@127.0.0.1:9911/1",
		Release:          "demo@1.2.0",
		Environment:      "test",
		AttachStacktrace: true,
		SendDefaultPII:   true,
	})
	if err != nil {
		panic(err)
	}
	defer sentry.Flush(5 * time.Second)

	sentry.ConfigureScope(func(s *sentry.Scope) {
		s.SetUser(sentry.User{ID: "42", Email: "user@example.com"})
		s.SetTag("feature", "checkout")
	})
	sentry.AddBreadcrumb(&sentry.Breadcrumb{Category: "ui", Message: "нажал «Оплатить»"})

	// 1. Ошибка с обёрткой.
	sentry.CaptureException(loadConfig("missing.yaml"))

	// 2. Сообщение.
	sentry.CaptureMessage("Order 17 failed")

	// 3. Паника.
	func() {
		defer func() {
			if r := recover(); r != nil {
				sentry.CurrentHub().Recover(r)
			}
		}()
		var m map[string]int
		m["x"] = 1
	}()

	// 4. errors.Join.
	sentry.CaptureException(errors.Join(errors.New("первая"), errors.New("вторая")))
}
