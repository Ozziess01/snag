// Package web вшивает собранный интерфейс (npm run build → dist/app)
// в бинарник snag: отдельный веб-сервер для него не нужен.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// all: — чтобы встроился и dist/.keep: без него папка пуста, пока
// интерфейс не собран, и go build падает.
//
//go:embed all:dist
var dist embed.FS

// Handler отдаёт интерфейс. Файлы из assets/ с хешем в имени кешируются
// надолго, index.html — нет, чтобы после обновления сразу подхватывался
// новый билд. Если интерфейс не собран, отвечает подсказкой.
func Handler() http.Handler {
	app, err := fs.Sub(dist, "dist/app")
	if err != nil {
		return notBuilt()
	}
	if _, err := fs.Stat(app, "index.html"); err != nil {
		return notBuilt()
	}
	files := http.FileServerFS(app)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		// Инлайн-стили нужны для высоты столбиков графиков (style="height").
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

func notBuilt() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("Интерфейс не собран: cd web && npm ci && npm run build, затем соберите snag заново.\n"))
	})
}
