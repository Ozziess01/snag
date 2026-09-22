// Команда snag — сервис приёма ошибок, совместимый с SDK Sentry.
//
//	snag serve                   запустить приём и интерфейс (по умолчанию)
//	snag migrate                 накатить миграции Postgres и ClickHouse
//	snag project create <name>   создать проект и напечатать DSN
//	snag user create <email>     создать пользователя (пароль из SNAG_PASSWORD
//	                             или со стандартного ввода)
//
// Настройки — переменные окружения, см. config().
package main

import (
	"bufio"
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

	"github.com/Ozziess01/snag/internal/api"
	"github.com/Ozziess01/snag/internal/ingest"
	"github.com/Ozziess01/snag/internal/notify"
	"github.com/Ozziess01/snag/internal/pipeline"
	"github.com/Ozziess01/snag/internal/store/ch"
	"github.com/Ozziess01/snag/internal/store/pg"
	"github.com/Ozziess01/snag/web"
)

type Config struct {
	Addr      string
	PublicURL string
	PgDSN     string
	ChDSN     string
	QueueSize int
	BatchSize int
	// Первый пользователь создаётся при запуске, если задан и его ещё нет:
	// удобно для docker compose и Codespaces.
	AdminEmail    string
	AdminPassword string
	// Токен бота Telegram (@BotFather). Пусто — уведомления выключены.
	TelegramToken string
	// Адрес Bot API: по умолчанию api.telegram.org, можно свой
	// (self-hosted telegram-bot-api) или фейковый для проверки.
	TelegramAPI string
}

func config() Config {
	return Config{
		Addr:      env("SNAG_ADDR", ":8000"),
		PublicURL: strings.TrimRight(env("SNAG_PUBLIC_URL", "http://localhost:8000"), "/"),
		PgDSN:     env("SNAG_PG_DSN", "postgres://snag:snag@127.0.0.1:15432/snag?sslmode=disable"),
		ChDSN:     env("SNAG_CH_DSN", "clickhouse://snag:snag@127.0.0.1:19000/snag"),
		QueueSize: envInt("SNAG_QUEUE_SIZE", 10_000),
		BatchSize: envInt("SNAG_BATCH_SIZE", 5_000),

		AdminEmail:    os.Getenv("SNAG_ADMIN_EMAIL"),
		AdminPassword: os.Getenv("SNAG_ADMIN_PASSWORD"),
		TelegramToken: os.Getenv("SNAG_TELEGRAM_TOKEN"),
		TelegramAPI:   os.Getenv("SNAG_TELEGRAM_API"),
	}
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	args := os.Args[1:]
	var err error
	switch {
	case len(args) == 0 || args[0] == "serve":
		err = serve(log, config())
	case args[0] == "migrate":
		err = migrate(context.Background(), config())
	case len(args) == 3 && args[0] == "project" && args[1] == "create":
		err = createProject(context.Background(), config(), args[2])
	case len(args) == 3 && args[0] == "user" && args[1] == "create":
		err = createUser(context.Background(), config(), args[2])
	default:
		fmt.Fprintln(os.Stderr, "использование: snag [serve | migrate | project create <name> | user create <email>]")
		os.Exit(2)
	}
	if err != nil {
		log.Error("snag", "err", err)
		os.Exit(1)
	}
}

func openStores(ctx context.Context, cfg Config) (*pg.Store, *ch.Store, error) {
	p, err := pg.Open(ctx, cfg.PgDSN)
	if err != nil {
		return nil, nil, err
	}
	c, err := ch.Open(ctx, cfg.ChDSN)
	if err != nil {
		p.Close()
		return nil, nil, err
	}
	return p, c, nil
}

func migrate(ctx context.Context, cfg Config) error {
	p, c, err := openStores(ctx, cfg)
	if err != nil {
		return err
	}
	defer p.Close()
	defer c.Close()
	if err := p.Migrate(ctx); err != nil {
		return err
	}
	return c.Migrate(ctx)
}

func createProject(ctx context.Context, cfg Config, name string) error {
	if err := migrate(ctx, cfg); err != nil {
		return err
	}
	p, err := pg.Open(ctx, cfg.PgDSN)
	if err != nil {
		return err
	}
	defer p.Close()
	np, err := p.CreateProject(ctx, name)
	if err != nil {
		return err
	}
	scheme, host, _ := strings.Cut(cfg.PublicURL, "://")
	fmt.Printf("Проект %q создан (id %d, slug %s)\nDSN: %s://%s@%s/%d\n", name, np.ID, np.Slug, scheme, np.PublicKey, host, np.ID)
	return nil
}

func createUser(ctx context.Context, cfg Config, email string) error {
	password := os.Getenv("SNAG_PASSWORD")
	if password == "" {
		fmt.Print("Пароль (от 8 символов): ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		password = strings.TrimSpace(line)
	}
	if err := migrate(ctx, cfg); err != nil {
		return err
	}
	p, err := pg.Open(ctx, cfg.PgDSN)
	if err != nil {
		return err
	}
	defer p.Close()
	u, err := p.CreateUser(ctx, email, password)
	if err != nil {
		return err
	}
	fmt.Printf("Пользователь %s готов (id %d)\n", u.Email, u.ID)
	return nil
}

// backend — API поверх двух баз: проекты и проблемы из Postgres,
// события и статистика из ClickHouse.
type backend struct {
	*pgBackend
	*chBackend
}

// Псевдонимы дают встроенным полям разные имена: у обоих типов имя Store.
type (
	pgBackend = pg.Store
	chBackend = ch.Store
)

func serve(log *slog.Logger, cfg Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pgStore, chStore, err := openStores(ctx, cfg)
	if err != nil {
		return err
	}
	defer pgStore.Close()
	defer chStore.Close()
	if err := pgStore.Migrate(ctx); err != nil {
		return err
	}
	if err := chStore.Migrate(ctx); err != nil {
		return err
	}
	if cfg.AdminEmail != "" && cfg.AdminPassword != "" {
		if _, _, err := pgStore.Login(ctx, cfg.AdminEmail, cfg.AdminPassword); err != nil {
			if _, err := pgStore.CreateUser(ctx, cfg.AdminEmail, cfg.AdminPassword); err != nil {
				return fmt.Errorf("SNAG_ADMIN_EMAIL: %w", err)
			}
			log.Info("создан пользователь", "email", cfg.AdminEmail)
		}
	}
	if ok, _ := pgStore.HasUsers(ctx); !ok {
		log.Warn("пользователей нет: создайте первого командой snag user create <email>")
	}

	notifier, botName := startTelegram(ctx, log, cfg, backend{pgStore, chStore})

	queue := pipeline.NewQueue(cfg.QueueSize)
	worker := &pipeline.Worker{
		Queue:      queue,
		Issues:     pgStore,
		Events:     chStore,
		Log:        log,
		BatchSize:  cfg.BatchSize,
		FlushEvery: time.Second,
		Retries:    4,
		OnFlush: func(ctx context.Context, activity []pipeline.Activity) {
			for _, a := range activity {
				switch {
				case a.Issue.Created:
					log.Info("новая проблема", "project", a.Issue.ProjectID, "issue", a.Issue.ID, "title", a.Event.Title())
				case a.Issue.Regressed:
					log.Info("проблема вернулась", "project", a.Issue.ProjectID, "issue", a.Issue.ID, "title", a.Event.Title())
				}
			}
			if notifier != nil {
				notifier.Handle(ctx, activity)
			}
		},
	}
	workerDone := make(chan struct{})
	go func() {
		worker.Run(context.Background())
		close(workerDone)
	}()

	h := &ingest.Handler{
		Projects:       pgStore,
		Sink:           queue,
		Log:            log,
		MaxBodySize:    20 << 20,
		MaxDecodedSize: 50 << 20,
		MaxEventSize:   1 << 20,
	}
	mux := http.NewServeMux()
	h.Register(mux)
	(&api.API{
		Backend:      backend{pgStore, chStore},
		Auth:         pgStore,
		Log:          log,
		PublicURL:    cfg.PublicURL,
		SecureCookie: strings.HasPrefix(cfg.PublicURL, "https://"),
		Notifier:     apiNotifier(notifier),
		BotName:      botName,
	}).Register(mux)
	mux.Handle("GET /", web.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "ok queue=%d written=%d dropped=%d\n", queue.Len(), worker.Stats.Written.Load(), worker.Stats.Dropped.Load())
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("snag слушает", "addr", cfg.Addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}

	// Порядок остановки: сначала перестаём принимать, потом закрываем
	// очередь и ждём, пока воркер допишет всё принятое.
	log.Info("останавливаюсь")
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	queue.Close()
	select {
	case <-workerDone:
	case <-time.After(30 * time.Second):
		log.Error("воркер не успел дописать очередь", "left", queue.Len())
	}
	log.Info("остановлен", "written", worker.Stats.Written.Load(), "dropped", worker.Stats.Dropped.Load())
	return nil
}

// startTelegram включает уведомления, если задан токен бота: проверяет
// токен (getMe), запускает отправку в фоне и ответы бота на /start.
func startTelegram(ctx context.Context, log *slog.Logger, cfg Config, st notify.Store) (*notify.Notifier, string) {
	if cfg.TelegramToken == "" {
		log.Info("уведомления в Telegram выключены: SNAG_TELEGRAM_TOKEN не задан")
		return nil, ""
	}
	tg := &notify.Telegram{Token: cfg.TelegramToken, BaseURL: strings.TrimRight(cfg.TelegramAPI, "/")}
	check, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	name, err := tg.Username(check)
	if err != nil {
		log.Error("Telegram: токен не подошёл, уведомления выключены", "err", err)
		return nil, ""
	}
	n := notify.New(st, tg, cfg.PublicURL, log)
	go n.Run(ctx)
	go tg.Poll(ctx, func(err error) { log.Warn("Telegram: чтение сообщений боту", "err", err) })
	log.Info("уведомления в Telegram включены", "bot", "@"+name)
	return n, name
}

// apiNotifier: nil-указатель в интерфейсе — не nil, поэтому явно.
func apiNotifier(n *notify.Notifier) api.Notifier {
	if n == nil {
		return nil
	}
	return n
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}
