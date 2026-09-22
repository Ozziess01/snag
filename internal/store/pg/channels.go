package pg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Ozziess01/snag/internal/store"
)

func (s *Store) Channels(ctx context.Context, projectID uint64) ([]store.Channel, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, project_id, kind, target, on_new, on_regression, spike_threshold, spike_window_minutes, created_at
		FROM notification_channels WHERE project_id = $1 ORDER BY id`, int64(projectID))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (store.Channel, error) {
		var c store.Channel
		var id, project int64
		err := r.Scan(&id, &project, &c.Kind, &c.Target, &c.OnNew, &c.OnRegression, &c.SpikeThreshold, &c.SpikeWindow, &c.CreatedAt)
		c.ID, c.ProjectID = uint64(id), uint64(project)
		return c, err
	})
}

// SaveChannel создаёт канал (ID = 0) или обновляет существующий.
func (s *Store) SaveChannel(ctx context.Context, c store.Channel) (store.Channel, error) {
	if err := c.Validate(); err != nil {
		return c, err
	}
	var id int64
	var err error
	if c.ID == 0 {
		err = s.pool.QueryRow(ctx, `
			INSERT INTO notification_channels (project_id, kind, target, on_new, on_regression, spike_threshold, spike_window_minutes)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id, created_at`,
			int64(c.ProjectID), c.Kind, c.Target, c.OnNew, c.OnRegression, c.SpikeThreshold, c.SpikeWindow).Scan(&id, &c.CreatedAt)
	} else {
		err = s.pool.QueryRow(ctx, `
			UPDATE notification_channels SET target = $3, on_new = $4, on_regression = $5,
			       spike_threshold = $6, spike_window_minutes = $7
			WHERE id = $1 AND project_id = $2 RETURNING id, created_at`,
			int64(c.ID), int64(c.ProjectID), c.Target, c.OnNew, c.OnRegression, c.SpikeThreshold, c.SpikeWindow).Scan(&id, &c.CreatedAt)
	}
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return c, store.ErrNotFound
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		return c, store.ErrDuplicate
	case err != nil:
		return c, err
	}
	c.ID = uint64(id)
	return c, nil
}

func (s *Store) DeleteChannel(ctx context.Context, projectID, id uint64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM notification_channels WHERE id = $1 AND project_id = $2`, int64(id), int64(projectID))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}
