// Package pg — хранилище в Postgres: проекты, ключи DSN, проблемы,
// пользователи и сессии.
package pg

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Ozziess01/snag/internal/ingest"
	"github.com/Ozziess01/snag/internal/store"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	pool *pgxpool.Pool
	keys keyCache
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pg: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: нет соединения: %w", err)
	}
	return &Store{pool: pool, keys: keyCache{items: map[string]cachedKey{}}}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Migrate применяет новые файлы из migrations/ по порядку имён. Advisory
// lock не даёт двум запущенным копиям накатывать миграции одновременно.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	const lockID = 0x736e6167 // "snag"
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, lockID) //nolint:errcheck

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	files, _ := fs.Glob(migrations, "migrations/*.sql")
	sort.Strings(files)
	for _, f := range files {
		version := strings.TrimSuffix(strings.TrimPrefix(f, "migrations/"), ".sql")
		var done bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		sql, _ := migrations.ReadFile(f)
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version)
			return err
		})
		if err != nil {
			return fmt.Errorf("pg: миграция %s: %w", version, err)
		}
	}
	return nil
}

// ---------- проекты ----------

var reSlug = regexp.MustCompile(`[^a-z0-9]+`)

// CreateProject создаёт проект и первый ключ DSN.
func (s *Store) CreateProject(ctx context.Context, name string) (store.Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return store.Project{}, errors.New("pg: пустое имя проекта")
	}
	slug := strings.Trim(reSlug.ReplaceAllString(translit(strings.ToLower(name)), "-"), "-")
	if slug == "" {
		slug = "project"
	}
	p := store.Project{Name: name, PublicKey: randomKey(), AllowedOrigins: []string{}, CreatedAt: time.Now().UTC()}
	var id int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Занятый slug не трогаем: пробуем тот же с коротким случайным хвостом.
		for attempt := 0; p.ID == 0; attempt++ {
			candidate := slug
			if attempt > 0 {
				candidate = slug + "-" + randomKey()[:4]
			}
			err := tx.QueryRow(ctx,
				`INSERT INTO projects (name, slug) VALUES ($1, $2) ON CONFLICT (slug) DO NOTHING RETURNING id, slug`,
				name, candidate).Scan(&id, &p.Slug)
			p.ID = uint64(id)
			switch {
			case errors.Is(err, pgx.ErrNoRows) && attempt < 5:
				continue
			case err != nil:
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO project_keys (public_key, project_id) VALUES ($1, $2)`, p.PublicKey, id)
		return err
	})
	return p, err
}

// ByKey реализует ingest.Projects. Ключ проверяется на каждом запросе SDK,
// поэтому ответ кешируется: найденный ключ на минуту, ненайденный на
// 10 секунд, чтобы мусорные ключи не долбили базу.
func (s *Store) ByKey(ctx context.Context, key string) (ingest.Project, error) {
	if key == "" || len(key) > 64 {
		return ingest.Project{}, ingest.ErrUnknownKey
	}
	if p, ok, hit := s.keys.get(key); hit {
		if !ok {
			return ingest.Project{}, ingest.ErrUnknownKey
		}
		return p, nil
	}
	var p ingest.Project
	err := s.pool.QueryRow(ctx,
		`SELECT p.id, p.allowed_origins FROM project_keys k JOIN projects p ON p.id = k.project_id
		 WHERE k.public_key = $1 AND k.active`, key).Scan(&p.ID, &p.AllowedOrigins)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		s.keys.put(key, ingest.Project{}, false, 10*time.Second)
		return ingest.Project{}, ingest.ErrUnknownKey
	case err != nil:
		return ingest.Project{}, err
	}
	s.keys.put(key, p, true, time.Minute)
	return p, nil
}

// ---------- проблемы ----------

// UpsertIssues создаёт или обновляет проблемы одним запросом на всю пачку.
//
// Два приёма Postgres:
//   - xmax = 0 у вставленной строки значит, что её только что создали,
//     а не обновили при гонке — так видно новую проблему без лишнего SELECT;
//   - now() одинаков для всего запроса, поэтому regressed_at = now()
//     в RETURNING значит «переоткрыта именно сейчас».
//
// Решённая проблема переоткрывается, только если событие случилось позже
// решения: запоздавший отчёт из прошлого не считается регрессией.
func (s *Store) UpsertIssues(ctx context.Context, deltas []store.IssueDelta) ([]store.IssueResult, error) {
	if len(deltas) == 0 {
		return nil, nil
	}
	// Одинаковый порядок строк во всех запросах исключает взаимные блокировки,
	// если воркеров станет несколько.
	sort.Slice(deltas, func(i, j int) bool {
		if deltas[i].ProjectID != deltas[j].ProjectID {
			return deltas[i].ProjectID < deltas[j].ProjectID
		}
		return deltas[i].Fingerprint < deltas[j].Fingerprint
	})

	n := len(deltas)
	var (
		projects, fps, counts   = make([]int64, n), make([]int64, n), make([]int64, n)
		kinds, titles, culprits = make([]string, n), make([]string, n), make([]string, n)
		levels, platforms       = make([]string, n), make([]string, n)
		firsts, lasts           = make([]time.Time, n), make([]time.Time, n)
	)
	for i, d := range deltas {
		projects[i], fps[i], counts[i] = int64(d.ProjectID), int64(d.Fingerprint), d.Count
		kinds[i], titles[i], culprits[i] = d.Kind, d.Title, d.Culprit
		levels[i], platforms[i] = d.Level, d.Platform
		firsts[i], lasts[i] = d.FirstSeen, d.LastSeen
	}

	// Сначала обновляем существующие проблемы и только потом вставляем
	// новые. Один INSERT … ON CONFLICT DO UPDATE тоже работает, но берёт
	// номер из последовательности на каждую строку, даже если строка
	// просто обновилась: после миллиона событий id новых проблем уходили
	// в десятки тысяч. Теперь номер тратится только на новый отпечаток.
	// Существующие строки блокируются в порядке (project_id, fingerprint):
	// несколько воркеров не устроят взаимную блокировку.
	rows, err := s.pool.Query(ctx, `
		WITH input AS (
		    SELECT * FROM unnest($1::bigint[], $2::bigint[], $3::text[], $4::text[], $5::text[], $6::text[],
		                         $7::text[], $8::timestamptz[], $9::timestamptz[], $10::bigint[])
		        AS t(project_id, fingerprint, grouping_kind, title, culprit, level, platform,
		             first_seen, last_seen, times_seen)
		),
		locked AS (
		    SELECT i.id FROM issues i
		    JOIN input t ON i.project_id = t.project_id AND i.fingerprint = t.fingerprint
		    ORDER BY i.project_id, i.fingerprint
		    FOR UPDATE OF i
		),
		upd AS (
		    UPDATE issues AS i SET
		        first_seen   = LEAST(i.first_seen, t.first_seen),
		        last_seen    = GREATEST(i.last_seen, t.last_seen),
		        times_seen   = i.times_seen + t.times_seen,
		        title        = t.title,
		        culprit      = t.culprit,
		        level        = t.level,
		        status       = CASE WHEN i.status = 'resolved' AND t.last_seen > i.resolved_at
		                            THEN 'unresolved' ELSE i.status END,
		        regressed_at = CASE WHEN i.status = 'resolved' AND t.last_seen > i.resolved_at
		                            THEN now() ELSE i.regressed_at END,
		        resolved_at  = CASE WHEN i.status = 'resolved' AND t.last_seen > i.resolved_at
		                            THEN NULL ELSE i.resolved_at END
		    FROM input t
		    WHERE i.id IN (SELECT id FROM locked)
		      AND i.project_id = t.project_id AND i.fingerprint = t.fingerprint
		    RETURNING i.id, i.project_id, i.fingerprint, false AS created,
		              coalesce(i.regressed_at = now(), false) AS regressed
		),
		ins AS (
		    -- Гонка: другой воркер мог вставить тот же отпечаток после снимка —
		    -- тогда ON CONFLICT обновит его строку.
		    INSERT INTO issues AS i (project_id, fingerprint, grouping_kind, title, culprit, level, platform,
		                             first_seen, last_seen, times_seen)
		    SELECT t.* FROM input t
		    WHERE NOT EXISTS (SELECT 1 FROM upd u WHERE u.project_id = t.project_id AND u.fingerprint = t.fingerprint)
		    ON CONFLICT (project_id, fingerprint) DO UPDATE SET
		        first_seen = LEAST(i.first_seen, EXCLUDED.first_seen),
		        last_seen  = GREATEST(i.last_seen, EXCLUDED.last_seen),
		        times_seen = i.times_seen + EXCLUDED.times_seen,
		        title      = EXCLUDED.title,
		        culprit    = EXCLUDED.culprit,
		        level      = EXCLUDED.level
		    RETURNING i.id, i.project_id, i.fingerprint, (i.xmax = 0) AS created, false AS regressed
		)
		SELECT * FROM upd UNION ALL SELECT * FROM ins`,
		projects, fps, kinds, titles, culprits, levels, platforms, firsts, lasts, counts)
	if err != nil {
		return nil, fmt.Errorf("pg: upsert issues: %w", err)
	}
	out := make([]store.IssueResult, 0, n)
	for rows.Next() {
		var r store.IssueResult
		var id, project, fp int64
		if err := rows.Scan(&id, &project, &fp, &r.Created, &r.Regressed); err != nil {
			return nil, err
		}
		r.ID, r.ProjectID, r.Fingerprint = uint64(id), uint64(project), uint64(fp)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetIssueStatus меняет статус проблемы (решена, игнорируется, открыта).
func (s *Store) SetIssueStatus(ctx context.Context, issueID uint64, status string) error {
	if !store.ValidStatus(status) {
		return fmt.Errorf("pg: неизвестный статус %q", status)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE issues SET status = $2,
		       resolved_at = CASE WHEN $2 = 'resolved' THEN now() ELSE NULL END
		WHERE id = $1`, int64(issueID), status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ---------- кеш ключей ----------

type cachedKey struct {
	project ingest.Project
	ok      bool
	expires time.Time
}

type keyCache struct {
	mu    sync.Mutex
	items map[string]cachedKey
}

func (c *keyCache) get(key string) (ingest.Project, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, found := c.items[key]
	if !found || time.Now().After(v.expires) {
		return ingest.Project{}, false, false
	}
	return v.project, v.ok, true
}

func (c *keyCache) put(key string, p ingest.Project, ok bool, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Защита от раздувания мусорными ключами: проще сбросить всё разом,
	// чем вести LRU, — кеш наполнится заново за секунды.
	if len(c.items) >= 10_000 {
		c.items = map[string]cachedKey{}
	}
	c.items[key] = cachedKey{project: p, ok: ok, expires: time.Now().Add(ttl)}
}

var ruLat = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z", 'и': "i",
	'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t",
	'у': "u", 'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "", 'ы': "y", 'ь': "",
	'э': "e", 'ю': "yu", 'я': "ya",
}

// translit: «Демо-магазин» → «demo-magazin», чтобы slug был читаемым.
func translit(s string) string {
	var b strings.Builder
	for _, r := range s {
		if lat, ok := ruLat[r]; ok {
			b.WriteString(lat)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func randomKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
