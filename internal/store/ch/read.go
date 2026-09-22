package ch

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Ozziess01/snag/internal/store"
)

// IssueStats — счётчики проблем проекта за период: всего событий,
// уникальных пользователей и события по интервалам для графика.
// issueID = 0 — по всем проблемам проекта. Читает почасовую таблицу,
// поэтому не зависит от числа сырых событий.
func (s *Store) IssueStats(ctx context.Context, projectID, issueID uint64, p store.Period) (map[uint64]store.Stats, error) {
	out := map[uint64]store.Stats{}
	n := p.Buckets()

	totals, err := s.conn.Query(ctx, `
		SELECT issue_id, sum(events), uniqMerge(users)
		FROM issue_hourly
		WHERE project_id = ? AND (? = 0 OR issue_id = ?) AND hour >= ? AND hour < ?
		GROUP BY issue_id`, projectID, issueID, issueID, p.Since, p.Until)
	if err != nil {
		return nil, err
	}
	for totals.Next() {
		var id uint64
		var st store.Stats
		if err := totals.Scan(&id, &st.Events, &st.Users); err != nil {
			totals.Close()
			return nil, err
		}
		st.Buckets = make([]uint64, n)
		out[id] = st
	}
	totals.Close()

	buckets, err := s.conn.Query(ctx, `
		SELECT issue_id, toUInt64(intDiv(toUInt32(hour) - toUInt32(?), ?)) AS b, sum(events)
		FROM issue_hourly
		WHERE project_id = ? AND (? = 0 OR issue_id = ?) AND hour >= ? AND hour < ?
		GROUP BY issue_id, b`, p.Since, uint32(p.Step.Seconds()), projectID, issueID, issueID, p.Since, p.Until)
	if err != nil {
		return nil, err
	}
	defer buckets.Close()
	for buckets.Next() {
		var id, b, events uint64
		if err := buckets.Scan(&id, &b, &events); err != nil {
			return nil, err
		}
		if st, ok := out[id]; ok && int(b) < n {
			st.Buckets[b] = events
		}
	}
	return out, buckets.Err()
}

// IssueTags — самые частые значения тегов проблемы: «Chrome — 70 %».
// Для каждого тега до limit значений и сколько событий с этим тегом всего.
func (s *Store) IssueTags(ctx context.Context, projectID, issueID uint64, limit int) (map[string][]store.TagValue, map[string]uint64, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT key, value, c, total FROM (
			SELECT key, value, count() AS c, sum(count()) OVER (PARTITION BY key) AS total
			FROM events
			ARRAY JOIN mapKeys(tags) AS key, mapValues(tags) AS value
			WHERE project_id = ? AND issue_id = ?
			GROUP BY key, value
		)
		ORDER BY key, c DESC, value
		LIMIT ? BY key`, projectID, issueID, limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	values, totals := map[string][]store.TagValue{}, map[string]uint64{}
	for rows.Next() {
		var key string
		var v store.TagValue
		var total uint64
		if err := rows.Scan(&key, &v.Value, &v.Count, &total); err != nil {
			return nil, nil, err
		}
		values[key] = append(values[key], v)
		totals[key] = total
	}
	return values, totals, rows.Err()
}

// IssueEvents — события проблемы от новых к старым, старше before.
func (s *Store) IssueEvents(ctx context.Context, projectID, issueID uint64, before time.Time, limit int) ([]store.EventSummary, error) {
	if before.IsZero() {
		before = time.Now().Add(24 * time.Hour)
	}
	rows, err := s.conn.Query(ctx, `
		SELECT replaceAll(toString(event_id), '-', ''), timestamp, level, release, environment, user_key
		FROM events
		WHERE project_id = ? AND issue_id = ? AND timestamp < toDateTime64(?, 3, 'UTC')
		ORDER BY timestamp DESC, event_id DESC
		LIMIT ?`, projectID, issueID, ms(before), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.EventSummary
	for rows.Next() {
		var e store.EventSummary
		if err := rows.Scan(&e.EventID, &e.Timestamp, &e.Level, &e.Release, &e.Environment, &e.UserKey); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

const eventColumns = `replaceAll(toString(event_id), '-', ''), timestamp, received_at, level, platform,
	environment, release, sdk, title, culprit, user_key, data`

func (s *Store) scanEvent(row interface{ Scan(...any) error }, projectID, issueID uint64) (store.Event, error) {
	e := store.Event{ProjectID: projectID, IssueID: issueID}
	err := row.Scan(&e.EventID, &e.Timestamp, &e.ReceivedAt, &e.Level, &e.Platform, &e.Environment,
		&e.Release, &e.SDK, &e.Title, &e.Culprit, &e.UserKey, &e.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return e, store.ErrNotFound
	}
	return e, err
}

// Event — событие проблемы по id; eventID = "latest" — самое свежее.
func (s *Store) Event(ctx context.Context, projectID, issueID uint64, eventID string) (store.Event, error) {
	if eventID == "latest" {
		return s.scanEvent(s.conn.QueryRow(ctx, `SELECT `+eventColumns+` FROM events
			WHERE project_id = ? AND issue_id = ? ORDER BY timestamp DESC, event_id DESC LIMIT 1`,
			projectID, issueID), projectID, issueID)
	}
	return s.scanEvent(s.conn.QueryRow(ctx, `SELECT `+eventColumns+` FROM events
		WHERE project_id = ? AND issue_id = ? AND event_id = toUUID(?) LIMIT 1`,
		projectID, issueID, uuidString(strings.ToLower(eventID))), projectID, issueID)
}

// Neighbors — соседние события для кнопок «старее» и «новее».
func (s *Store) Neighbors(ctx context.Context, projectID, issueID uint64, ev store.Event) (older, newer string, err error) {
	id, ts := uuidString(ev.EventID), ms(ev.Timestamp)
	one := func(cond, order string) (string, error) {
		var v string
		err := s.conn.QueryRow(ctx, `SELECT replaceAll(toString(event_id), '-', '') FROM events
			WHERE project_id = ? AND issue_id = ? AND event_id != toUUID(?) AND `+cond+` ORDER BY `+order+` LIMIT 1`,
			projectID, issueID, id, ts, ts, id).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return v, err
	}
	const t = `toDateTime64(?, 3, 'UTC')`
	if older, err = one(`(timestamp < `+t+` OR (timestamp = `+t+` AND event_id < toUUID(?)))`, `timestamp DESC, event_id DESC`); err != nil {
		return "", "", err
	}
	newer, err = one(`(timestamp > `+t+` OR (timestamp = `+t+` AND event_id > toUUID(?)))`, `timestamp, event_id`)
	return older, newer, err
}

// ms — время строкой с миллисекундами. time.Time драйвер передаёт
// с точностью до секунды, а события хранятся с миллисекундами: без этого
// событие 12:00:00.500 оказывалось «новее» самого себя.
func ms(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05.000")
}
