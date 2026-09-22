// Package mem — всё хранилище Snag в памяти процесса. Нужно для демо,
// которое целиком работает в браузере (WebAssembly, баз там нет), и для
// тестов API. Повторяет поведение pg и ch, включая регрессии и счётчики.
package mem

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Ozziess01/snag/internal/ingest"
	"github.com/Ozziess01/snag/internal/store"
)

type Store struct {
	mu       sync.RWMutex
	projects []store.Project
	keys     map[string]uint64
	issues   []*store.Issue
	byFP     map[[2]uint64]*store.Issue
	events   []store.Event
	users    map[string]memUser
	sessions map[string]uint64
	// MaxEvents — сколько событий держать; самые старые вытесняются.
	MaxEvents int
}

type memUser struct {
	store.User
	hash string
}

func New() *Store {
	return &Store{
		keys:      map[string]uint64{},
		byFP:      map[[2]uint64]*store.Issue{},
		users:     map[string]memUser{},
		sessions:  map[string]uint64{},
		MaxEvents: 20_000,
	}
}

// ---------- проекты ----------

func (s *Store) CreateProject(_ context.Context, name string) (store.Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return store.Project{}, errors.New("mem: пустое имя проекта")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var b [16]byte
	_, _ = rand.Read(b[:])
	p := store.Project{
		ID: uint64(len(s.projects) + 1), Name: name, Slug: strings.ToLower(strings.ReplaceAll(name, " ", "-")),
		PublicKey: hex.EncodeToString(b[:]), AllowedOrigins: []string{}, CreatedAt: time.Now().UTC(),
	}
	s.projects = append(s.projects, p)
	s.keys[p.PublicKey] = p.ID
	return p, nil
}

func (s *Store) Projects(context.Context) ([]store.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]store.Project(nil), s.projects...), nil
}

func (s *Store) Project(_ context.Context, id uint64) (store.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.projects {
		if p.ID == id {
			return p, nil
		}
	}
	return store.Project{}, store.ErrNotFound
}

func (s *Store) ByKey(_ context.Context, key string) (ingest.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.keys[key]
	if !ok {
		return ingest.Project{}, ingest.ErrUnknownKey
	}
	return ingest.Project{ID: id}, nil
}

// ---------- проблемы ----------

func (s *Store) UpsertIssues(_ context.Context, deltas []store.IssueDelta) ([]store.IssueResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	out := make([]store.IssueResult, 0, len(deltas))
	for _, d := range deltas {
		k := [2]uint64{d.ProjectID, d.Fingerprint}
		i, ok := s.byFP[k]
		r := store.IssueResult{ProjectID: d.ProjectID, Fingerprint: d.Fingerprint}
		if !ok {
			i = &store.Issue{ID: uint64(len(s.issues) + 1), ProjectID: d.ProjectID, Kind: d.Kind, Status: store.StatusUnresolved,
				FirstSeen: d.FirstSeen, LastSeen: d.LastSeen}
			s.issues = append(s.issues, i)
			s.byFP[k] = i
			r.Created = true
		}
		if d.FirstSeen.Before(i.FirstSeen) {
			i.FirstSeen = d.FirstSeen
		}
		if d.LastSeen.After(i.LastSeen) {
			i.LastSeen = d.LastSeen
		}
		i.TimesSeen += d.Count
		i.Title, i.Culprit, i.Level, i.Platform = d.Title, d.Culprit, d.Level, d.Platform
		if i.Status == store.StatusResolved && i.ResolvedAt != nil && d.LastSeen.After(*i.ResolvedAt) {
			i.Status, i.ResolvedAt, i.RegressedAt = store.StatusUnresolved, nil, &now
			r.Regressed = true
		}
		r.ID = i.ID
		out = append(out, r)
	}
	return out, nil
}

func (s *Store) Issues(_ context.Context, f store.IssueFilter) ([]store.Issue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []store.Issue
	for _, i := range s.issues {
		if i.ProjectID == f.ProjectID && (f.Status == "" || i.Status == f.Status) {
			out = append(out, *i)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].LastSeen.After(out[b].LastSeen) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (s *Store) Issue(_ context.Context, id uint64) (store.Issue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id == 0 || id > uint64(len(s.issues)) {
		return store.Issue{}, store.ErrNotFound
	}
	return *s.issues[id-1], nil
}

func (s *Store) SetIssueStatus(_ context.Context, id uint64, status string) error {
	if !store.ValidStatus(status) {
		return errors.New("mem: неизвестный статус")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == 0 || id > uint64(len(s.issues)) {
		return store.ErrNotFound
	}
	i := s.issues[id-1]
	i.Status, i.ResolvedAt = status, nil
	if status == store.StatusResolved {
		now := time.Now().UTC()
		i.ResolvedAt = &now
	}
	return nil
}

func (s *Store) OpenIssues(context.Context) (map[uint64]int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[uint64]int64{}
	for _, i := range s.issues {
		if i.Status == store.StatusUnresolved {
			out[i.ProjectID]++
		}
	}
	return out, nil
}

// ---------- события ----------

func (s *Store) InsertEvents(_ context.Context, events []store.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, events...)
	if over := len(s.events) - s.MaxEvents; s.MaxEvents > 0 && over > 0 {
		s.events = append([]store.Event(nil), s.events[over:]...)
	}
	return nil
}

func (s *Store) IssueStats(_ context.Context, projectID, issueID uint64, p store.Period) (map[uint64]store.Stats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[uint64]store.Stats{}
	users := map[uint64]map[string]bool{}
	n := p.Buckets()
	for _, e := range s.events {
		if e.ProjectID != projectID || (issueID != 0 && e.IssueID != issueID) {
			continue
		}
		hour := e.Timestamp.Truncate(time.Hour)
		if hour.Before(p.Since) || !hour.Before(p.Until) {
			continue
		}
		st, ok := out[e.IssueID]
		if !ok {
			st.Buckets = make([]uint64, n)
			users[e.IssueID] = map[string]bool{}
		}
		st.Events++
		if b := int(hour.Sub(p.Since) / p.Step); b < n {
			st.Buckets[b]++
		}
		if e.UserKey != "" {
			users[e.IssueID][e.UserKey] = true
		}
		out[e.IssueID] = st
	}
	for id, st := range out {
		st.Users = uint64(len(users[id]))
		out[id] = st
	}
	return out, nil
}

func (s *Store) IssueTags(_ context.Context, projectID, issueID uint64, limit int) (map[string][]store.TagValue, map[string]uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := map[string]map[string]uint64{}
	totals := map[string]uint64{}
	for _, e := range s.events {
		if e.ProjectID != projectID || e.IssueID != issueID {
			continue
		}
		for k, v := range e.Tags {
			if counts[k] == nil {
				counts[k] = map[string]uint64{}
			}
			counts[k][v]++
			totals[k]++
		}
	}
	out := map[string][]store.TagValue{}
	for k, vals := range counts {
		list := make([]store.TagValue, 0, len(vals))
		for v, c := range vals {
			list = append(list, store.TagValue{Value: v, Count: c})
		}
		sort.Slice(list, func(a, b int) bool {
			if list[a].Count != list[b].Count {
				return list[a].Count > list[b].Count
			}
			return list[a].Value < list[b].Value
		})
		if len(list) > limit {
			list = list[:limit]
		}
		out[k] = list
	}
	return out, totals, nil
}

// issueEvents — события проблемы от новых к старым.
func (s *Store) issueEvents(projectID, issueID uint64) []store.Event {
	var list []store.Event
	for _, e := range s.events {
		if e.ProjectID == projectID && e.IssueID == issueID {
			list = append(list, e)
		}
	}
	sort.SliceStable(list, func(a, b int) bool {
		if !list[a].Timestamp.Equal(list[b].Timestamp) {
			return list[a].Timestamp.After(list[b].Timestamp)
		}
		return list[a].EventID > list[b].EventID
	})
	return list
}

func (s *Store) IssueEvents(_ context.Context, projectID, issueID uint64, before time.Time, limit int) ([]store.EventSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []store.EventSummary
	for _, e := range s.issueEvents(projectID, issueID) {
		if !before.IsZero() && !e.Timestamp.Before(before) {
			continue
		}
		out = append(out, store.EventSummary{EventID: e.EventID, Timestamp: e.Timestamp, Level: e.Level,
			Release: e.Release, Environment: e.Environment, UserKey: e.UserKey})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (s *Store) Event(_ context.Context, projectID, issueID uint64, eventID string) (store.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.issueEvents(projectID, issueID)
	for _, e := range list {
		if eventID == "latest" || e.EventID == eventID {
			return e, nil
		}
	}
	return store.Event{}, store.ErrNotFound
}

func (s *Store) Neighbors(_ context.Context, projectID, issueID uint64, ev store.Event) (older, newer string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.issueEvents(projectID, issueID)
	for i, e := range list {
		if e.EventID != ev.EventID {
			continue
		}
		if i+1 < len(list) {
			older = list[i+1].EventID
		}
		if i > 0 {
			newer = list[i-1].EventID
		}
		break
	}
	return older, newer, nil
}

// ---------- пользователи ----------

func (s *Store) CreateUser(_ context.Context, email, password string) (store.User, error) {
	hash, err := store.HashPassword(password)
	if err != nil {
		return store.User{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	email = store.NormalizeEmail(email)
	u := memUser{User: store.User{ID: uint64(len(s.users) + 1), Email: email}, hash: hash}
	s.users[email] = u
	return u.User, nil
}

func (s *Store) Login(_ context.Context, email, password string) (string, store.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.users[store.NormalizeEmail(email)]
	if !store.CheckPassword(u.hash, password) {
		return "", store.User{}, store.ErrBadCredentials
	}
	token, _ := store.NewSessionToken()
	s.sessions[token] = u.ID
	return token, u.User, nil
}

func (s *Store) UserBySession(_ context.Context, token string) (store.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.sessions[token]
	if !ok {
		return store.User{}, store.ErrNoSession
	}
	for _, u := range s.users {
		if u.ID == id {
			return u.User, nil
		}
	}
	return store.User{}, store.ErrNoSession
}

func (s *Store) Logout(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
	return nil
}
