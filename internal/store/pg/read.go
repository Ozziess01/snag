package pg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Ozziess01/snag/internal/store"
)

// ---------- проекты ----------

func (s *Store) Projects(ctx context.Context) ([]store.Project, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.name, p.slug, p.allowed_origins, p.created_at,
		       coalesce((SELECT k.public_key FROM project_keys k
		                 WHERE k.project_id = p.id AND k.active ORDER BY k.created_at LIMIT 1), '')
		FROM projects p ORDER BY p.name, p.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (store.Project, error) {
		var p store.Project
		var id int64
		err := r.Scan(&id, &p.Name, &p.Slug, &p.AllowedOrigins, &p.CreatedAt, &p.PublicKey)
		p.ID = uint64(id)
		return p, err
	})
}

func (s *Store) Project(ctx context.Context, id uint64) (store.Project, error) {
	list, err := s.Projects(ctx)
	if err != nil {
		return store.Project{}, err
	}
	for _, p := range list {
		if p.ID == id {
			return p, nil
		}
	}
	return store.Project{}, store.ErrNotFound
}

// ---------- проблемы ----------

const issueColumns = `id, project_id, grouping_kind, title, culprit, level, platform, status,
	first_seen, last_seen, times_seen, resolved_at, regressed_at`

func scanIssue(r pgx.Row) (store.Issue, error) {
	var i store.Issue
	var id, project int64
	err := r.Scan(&id, &project, &i.Kind, &i.Title, &i.Culprit, &i.Level, &i.Platform, &i.Status,
		&i.FirstSeen, &i.LastSeen, &i.TimesSeen, &i.ResolvedAt, &i.RegressedAt)
	i.ID, i.ProjectID = uint64(id), uint64(project)
	return i, err
}

func (s *Store) Issues(ctx context.Context, f store.IssueFilter) ([]store.Issue, error) {
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.pool.Query(ctx, `SELECT `+issueColumns+` FROM issues
		WHERE project_id = $1 AND ($2 = '' OR status = $2)
		ORDER BY last_seen DESC LIMIT $3`, int64(f.ProjectID), f.Status, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (store.Issue, error) { return scanIssue(r) })
}

func (s *Store) Issue(ctx context.Context, id uint64) (store.Issue, error) {
	i, err := scanIssue(s.pool.QueryRow(ctx, `SELECT `+issueColumns+` FROM issues WHERE id = $1`, int64(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Issue{}, store.ErrNotFound
	}
	return i, err
}

// OpenIssues — сколько открытых проблем в каждом проекте.
func (s *Store) OpenIssues(ctx context.Context) (map[uint64]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT project_id, count(*) FROM issues WHERE status = 'unresolved' GROUP BY project_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uint64]int64{}
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[uint64(id)] = n
	}
	return out, rows.Err()
}

// ---------- пользователи и сессии ----------

// CreateUser создаёт пользователя или меняет пароль существующему.
func (s *Store) CreateUser(ctx context.Context, email, password string) (store.User, error) {
	hash, err := store.HashPassword(password)
	if err != nil {
		return store.User{}, err
	}
	u := store.User{Email: store.NormalizeEmail(email)}
	var id int64
	err = s.pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash) VALUES ($1, $2)
		ON CONFLICT (email) DO UPDATE SET password_hash = EXCLUDED.password_hash
		RETURNING id`, u.Email, hash).Scan(&id)
	u.ID = uint64(id)
	return u, err
}

func (s *Store) HasUsers(ctx context.Context) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&ok)
	return ok, err
}

// Login проверяет пароль и открывает сессию.
func (s *Store) Login(ctx context.Context, email, password string) (string, store.User, error) {
	u := store.User{Email: store.NormalizeEmail(email)}
	var id int64
	var hash string
	err := s.pool.QueryRow(ctx, `SELECT id, password_hash FROM users WHERE email = $1`, u.Email).Scan(&id, &hash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", store.User{}, err
	}
	if !store.CheckPassword(hash, password) {
		return "", store.User{}, store.ErrBadCredentials
	}
	u.ID = uint64(id)
	token, tokenHash := store.NewSessionToken()
	_, err = s.pool.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		tokenHash, id, time.Now().Add(store.SessionTTL))
	return token, u, err
}

func (s *Store) UserBySession(ctx context.Context, token string) (store.User, error) {
	var u store.User
	var id int64
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.email FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, store.HashToken(token)).Scan(&id, &u.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.User{}, store.ErrNoSession
	}
	u.ID = uint64(id)
	return u, err
}

func (s *Store) Logout(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1 OR expires_at < now()`, store.HashToken(token))
	return err
}
