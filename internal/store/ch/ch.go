// Package ch — хранилище событий в ClickHouse.
package ch

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/Ozziess01/snag/internal/store"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	conn driver.Conn
}

// Open подключается по DSN вида clickhouse://user:pass@host:9000/db.
func Open(ctx context.Context, dsn string) (*Store, error) {
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("ch: %w", err)
	}
	opts.Compression = &clickhouse.Compression{Method: clickhouse.CompressionLZ4}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("ch: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ch: нет соединения: %w", err)
	}
	return &Store{conn: conn}, nil
}

func (s *Store) Close() error { return s.conn.Close() }

// Migrate применяет файлы migrations/ по порядку. Транзакций в ClickHouse
// нет, поэтому сами миграции пишутся идемпотентными (IF NOT EXISTS).
func (s *Store) Migrate(ctx context.Context) error {
	if err := s.conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations
		(version String, applied_at DateTime DEFAULT now()) ENGINE = ReplacingMergeTree ORDER BY version`); err != nil {
		return err
	}
	files, _ := fs.Glob(migrations, "migrations/*.sql")
	sort.Strings(files)
	for _, f := range files {
		version := strings.TrimSuffix(strings.TrimPrefix(f, "migrations/"), ".sql")
		var n uint64
		if err := s.conn.QueryRow(ctx, `SELECT count() FROM schema_migrations WHERE version = ?`, version).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		raw, _ := migrations.ReadFile(f)
		for _, stmt := range splitStatements(string(raw)) {
			if err := s.conn.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("ch: миграция %s: %w", version, err)
			}
		}
		if err := s.conn.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
			return err
		}
	}
	return nil
}

// InsertEvents пишет пачку одной вставкой. ClickHouse любит редкие большие
// вставки: каждая создаёт на диске «кусок», и тысячи вставок по строке
// быстро упираются в «too many parts».
func (s *Store) InsertEvents(ctx context.Context, events []store.Event) error {
	if len(events) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO events
		(project_id, issue_id, event_id, timestamp, received_at, level, platform, environment,
		 release, sdk, title, culprit, user_key, tags, data)`)
	if err != nil {
		return fmt.Errorf("ch: %w", err)
	}
	for _, e := range events {
		tags := e.Tags
		if tags == nil {
			tags = map[string]string{}
		}
		if err := batch.Append(e.ProjectID, e.IssueID, uuidString(e.EventID), e.Timestamp, e.ReceivedAt,
			e.Level, e.Platform, e.Environment, e.Release, e.SDK, e.Title, e.Culprit, e.UserKey, tags, e.Data); err != nil {
			_ = batch.Abort()
			return fmt.Errorf("ch: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("ch: вставка %d событий: %w", len(events), err)
	}
	return nil
}

// uuidString: 9ec79c33ec99… → 9ec79c33-ec99-…, как ждёт тип UUID.
func uuidString(hex32 string) string {
	if len(hex32) != 32 {
		return hex32
	}
	return hex32[0:8] + "-" + hex32[8:12] + "-" + hex32[12:16] + "-" + hex32[16:20] + "-" + hex32[20:32]
}

// splitStatements делит файл миграции на запросы по «;» в конце строки
// и выбрасывает комментарии.
func splitStatements(sql string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
		if strings.HasSuffix(trimmed, ";") {
			if stmt := strings.TrimSuffix(strings.TrimSpace(cur.String()), ";"); stmt != "" {
				out = append(out, stmt)
			}
			cur.Reset()
		}
	}
	if stmt := strings.TrimSpace(cur.String()); stmt != "" {
		out = append(out, stmt)
	}
	return out
}
