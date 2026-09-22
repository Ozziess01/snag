// Команда snag — сервис приёма ошибок, совместимый с SDK Sentry.
//
// Пока без базы: проекты задаются переменной окружения, принятые события
// пишутся в лог. Хранилище появится следующим шагом.
//
//	SNAG_ADDR=:8000 SNAG_PROJECTS=1:publickey,2:otherkey snag
//
// DSN для SDK: http://publickey@localhost:8000/1
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Ozziess01/snag/internal/ingest"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("snag остановлен", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	addr := env("SNAG_ADDR", ":8000")
	projects, err := parseProjects(env("SNAG_PROJECTS", ""))
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		return errors.New("не задан ни один проект: SNAG_PROJECTS=1:publickey")
	}

	h := &ingest.Handler{
		Projects:       projects,
		Sink:           logSink{log},
		Log:            log,
		MaxBodySize:    20 << 20,
		MaxDecodedSize: 50 << 20,
		MaxEventSize:   1 << 20,
	}
	mux := http.NewServeMux()
	h.Register(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		log.Info("snag слушает", "addr", addr, "projects", len(projects))
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("останавливаюсь")
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// staticProjects — проекты из переменной окружения, пока нет базы.
type staticProjects map[string]ingest.Project

func (s staticProjects) ByKey(_ context.Context, key string) (ingest.Project, bool) {
	p, ok := s[key]
	return p, ok
}

func parseProjects(spec string) (staticProjects, error) {
	out := staticProjects{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idStr, key, ok := strings.Cut(part, ":")
		id, err := strconv.ParseUint(idStr, 10, 64)
		if !ok || err != nil || key == "" {
			return nil, fmt.Errorf("SNAG_PROJECTS: ждём id:ключ, пришло %q", part)
		}
		out[key] = ingest.Project{ID: id}
	}
	return out, nil
}

// logSink пишет короткую сводку по событию в лог.
type logSink struct{ log *slog.Logger }

func (s logSink) Accept(_ context.Context, a ingest.Accepted) error {
	e := a.Event
	sdk := ""
	if e.SDK != nil {
		sdk = e.SDK.Name + "/" + e.SDK.Version
	}
	s.log.Info("событие",
		"project", a.ProjectID,
		"id", e.EventID,
		"level", e.Level,
		"title", e.Title(),
		"culprit", e.Culprit(),
		"platform", e.Platform,
		"sdk", sdk,
	)
	return nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
