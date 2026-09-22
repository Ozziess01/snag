//go:build js && wasm

// Команда snag-wasm — Snag, собранный в WebAssembly для демо в браузере.
//
// Внутри тот же код, что на сервере: приём событий, группировка, конвейер
// и API. Отличие одно: хранилище в памяти вкладки (store/mem) вместо
// Postgres и ClickHouse. Наружу выставляется window.snag.fetch(method, url,
// headers, body) → Promise<{status, body}>: через него интерфейс ходит в API,
// а браузерный SDK Sentry отправляет конверты на приём — всё без сети.
//
//	GOOS=js GOARCH=wasm go build -o snag.wasm ./cmd/snag-wasm
package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall/js"
	"time"

	"github.com/Ozziess01/snag/internal/api"
	"github.com/Ozziess01/snag/internal/ingest"
	"github.com/Ozziess01/snag/internal/pipeline"
	"github.com/Ozziess01/snag/internal/store/mem"
)

// Адрес приёма в DSN демо. В сеть по нему никто не ходит: SDK отправляет
// через window.snag.fetch, а тот смотрит только на путь.
const publicURL = "https://snag.demo"

func main() {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	st := mem.New()
	project, _ := st.CreateProject(ctx, "Демо-магазин")

	queue := pipeline.NewQueue(1000)
	worker := &pipeline.Worker{
		Queue: queue, Issues: st, Events: st, Log: log,
		BatchSize: 500, FlushEvery: 25 * time.Millisecond,
	}
	go worker.Run(ctx)

	mux := http.NewServeMux()
	(&ingest.Handler{
		Projects: st, Sink: queue, Log: log,
		MaxBodySize: 5 << 20, MaxDecodedSize: 10 << 20, MaxEventSize: 1 << 20,
	}).Register(mux)
	(&api.API{Backend: st, Log: log, PublicURL: publicURL}).Register(mux)

	seeded := seed(mux, project.ID, project.PublicKey, time.Now())
	// Ждём, пока воркер разложит историю по проблемам, и только потом
	// сообщаем странице, что можно показывать интерфейс.
	for deadline := time.Now().Add(10 * time.Second); worker.Stats.Written.Load() < int64(seeded) && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	resolveSeeded(ctx, st, project.ID)

	dsn := strings.Replace(publicURL, "://", "://"+project.PublicKey+"@", 1) + "/1"
	js.Global().Set("snag", js.ValueOf(map[string]any{
		"demo":    true,
		"fetch":   js.FuncOf(fetchFunc(mux)),
		"dsn":     dsn,
		"project": int(project.ID),
		"events":  seeded,
	}))
	js.Global().Call("dispatchEvent", js.Global().Get("Event").New("snag:ready"))

	select {} // WebAssembly-программа живёт, пока открыта вкладка
}

// fetchFunc превращает вызов из JavaScript в обычный HTTP-запрос к mux
// и отдаёт Promise с ответом. Обработка идёт в отдельной горутине: вызов
// из JS не должен блокировать поток страницы.
func fetchFunc(h http.Handler) func(js.Value, []js.Value) any {
	return func(_ js.Value, args []js.Value) any {
		method, url := args[0].String(), args[1].String()
		headers := map[string]string{}
		if len(args) > 2 && args[2].Type() == js.TypeObject {
			keys := js.Global().Get("Object").Call("keys", args[2])
			for i := 0; i < keys.Length(); i++ {
				k := keys.Index(i).String()
				headers[k] = args[2].Get(k).String()
			}
		}
		body := ""
		if len(args) > 3 && args[3].Type() == js.TypeString {
			body = args[3].String()
		}

		return js.Global().Get("Promise").New(js.FuncOf(func(_ js.Value, p []js.Value) any {
			resolve := p[0]
			go func() {
				r := httptest.NewRequest(method, url, strings.NewReader(body))
				r.RemoteAddr = "127.0.0.1:1"
				for k, v := range headers {
					r.Header.Set(k, v)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				resolve.Invoke(js.ValueOf(map[string]any{"status": w.Code, "body": w.Body.String()}))
			}()
			return nil
		}))
	}
}
