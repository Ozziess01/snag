// Package api — JSON API для веб-интерфейса Snag.
//
// Один и тот же код обслуживает интерфейс на сервере (Backend поверх
// Postgres и ClickHouse) и в демо, которое целиком работает в браузере
// (Backend в памяти).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Ozziess01/snag/internal/store"
)

// Backend — всё, что интерфейсу нужно от хранилищ.
type Backend interface {
	Projects(ctx context.Context) ([]store.Project, error)
	Project(ctx context.Context, id uint64) (store.Project, error)
	CreateProject(ctx context.Context, name string) (store.Project, error)
	OpenIssues(ctx context.Context) (map[uint64]int64, error)

	Issues(ctx context.Context, f store.IssueFilter) ([]store.Issue, error)
	Issue(ctx context.Context, id uint64) (store.Issue, error)
	SetIssueStatus(ctx context.Context, id uint64, status string) error

	IssueStats(ctx context.Context, projectID, issueID uint64, p store.Period) (map[uint64]store.Stats, error)
	IssueTags(ctx context.Context, projectID, issueID uint64, limit int) (map[string][]store.TagValue, map[string]uint64, error)
	IssueEvents(ctx context.Context, projectID, issueID uint64, before time.Time, limit int) ([]store.EventSummary, error)
	Event(ctx context.Context, projectID, issueID uint64, eventID string) (store.Event, error)
	Neighbors(ctx context.Context, projectID, issueID uint64, ev store.Event) (older, newer string, err error)

	ChannelStore
}

type Auth interface {
	Login(ctx context.Context, email, password string) (string, store.User, error)
	UserBySession(ctx context.Context, token string) (store.User, error)
	Logout(ctx context.Context, token string) error
}

type API struct {
	Backend Backend
	// Auth = nil — вход отключён (демо в браузере): все запросы от демо-пользователя.
	Auth         Auth
	Log          *slog.Logger
	Now          func() time.Time
	PublicURL    string // адрес приёма для DSN: https://snag.example
	SecureCookie bool
	Notifier     Notifier // nil — Telegram не настроен
	BotName      string   // имя бота для подсказки в интерфейсе

	limiter loginLimiter
}

const (
	cookieName = "snag_session"
	// Все запросы, меняющие данные, должны нести этот заголовок. Чужая
	// страница не может поставить его без CORS-разрешения, так что
	// подделать запрос от имени вошедшего пользователя (CSRF) нельзя.
	csrfHeader = "X-Requested-With"
	csrfValue  = "snag"
)

var demoUser = store.User{ID: 1, Email: "demo@snag.local"}

func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", a.login)
	mux.HandleFunc("POST /api/v1/auth/logout", a.logout)
	mux.HandleFunc("GET /api/v1/auth/me", a.authed(a.me))

	mux.HandleFunc("GET /api/v1/projects", a.authed(a.listProjects))
	mux.HandleFunc("POST /api/v1/projects", a.authed(a.createProject))
	mux.HandleFunc("GET /api/v1/projects/{project}/issues", a.authed(a.listIssues))

	mux.HandleFunc("GET /api/v1/issues/{issue}", a.authed(a.getIssue))
	mux.HandleFunc("PUT /api/v1/issues/{issue}", a.authed(a.updateIssue))
	mux.HandleFunc("GET /api/v1/issues/{issue}/events", a.authed(a.listEvents))
	mux.HandleFunc("GET /api/v1/issues/{issue}/events/{event}", a.authed(a.getEvent))
	a.registerChannels(mux)
}

// ---------- вход ----------

type userKey struct{}

func (a *API) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Header.Get(csrfHeader) != csrfValue {
			a.fail(w, http.StatusForbidden, "нет заголовка "+csrfHeader)
			return
		}
		user := demoUser
		if a.Auth != nil {
			c, err := r.Cookie(cookieName)
			if err != nil {
				a.fail(w, http.StatusUnauthorized, "нужно войти")
				return
			}
			user, err = a.Auth.UserBySession(r.Context(), c.Value)
			if errors.Is(err, store.ErrNoSession) {
				a.fail(w, http.StatusUnauthorized, "сессия истекла, войдите снова")
				return
			}
			if err != nil {
				a.internal(w, err)
				return
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	}
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(csrfHeader) != csrfValue {
		a.fail(w, http.StatusForbidden, "нет заголовка "+csrfHeader)
		return
	}
	if a.Auth == nil {
		writeJSON(w, http.StatusOK, map[string]any{"user": demoUser})
		return
	}
	var req struct{ Email, Password string }
	if !a.read(w, r, &req) {
		return
	}
	ip := remoteIP(r)
	if wait := a.limiter.blocked(ip, a.now()); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		a.fail(w, http.StatusTooManyRequests, "слишком много попыток входа, подождите минуту")
		return
	}
	token, user, err := a.Auth.Login(r.Context(), req.Email, req.Password)
	if errors.Is(err, store.ErrBadCredentials) {
		a.limiter.fail(ip, a.now())
		a.fail(w, http.StatusUnauthorized, "неверная почта или пароль")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	a.limiter.reset(ip)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: a.SecureCookie,
		SameSite: http.SameSiteLaxMode, MaxAge: int(store.SessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(csrfHeader) != csrfValue {
		a.fail(w, http.StatusForbidden, "нет заголовка "+csrfHeader)
		return
	}
	if c, err := r.Cookie(cookieName); err == nil && a.Auth != nil {
		_ = a.Auth.Logout(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, Secure: a.SecureCookie, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"user": r.Context().Value(userKey{}),
		"demo": a.Auth == nil,
	})
}

// ---------- проекты ----------

type projectDTO struct {
	store.Project
	DSN        string `json:"dsn"`
	OpenIssues int64  `json:"open_issues"`
}

func (a *API) projectDTO(p store.Project, open int64) projectDTO {
	scheme, host, ok := strings.Cut(a.PublicURL, "://")
	if !ok {
		scheme, host = "http", a.PublicURL
	}
	return projectDTO{Project: p, DSN: scheme + "://" + p.PublicKey + "@" + host + "/" + strconv.FormatUint(p.ID, 10), OpenIssues: open}
}

func (a *API) listProjects(w http.ResponseWriter, r *http.Request) {
	list, err := a.Backend.Projects(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	open, err := a.Backend.OpenIssues(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	out := make([]projectDTO, len(list))
	for i, p := range list {
		out[i] = a.projectDTO(p, open[p.ID])
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

func (a *API) createProject(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string }
	if !a.read(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 100 {
		a.fail(w, http.StatusBadRequest, "имя проекта: от 1 до 100 символов")
		return
	}
	p, err := a.Backend.CreateProject(r.Context(), req.Name)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": a.projectDTO(p, 0)})
}

// ---------- проблемы ----------

type issueDTO struct {
	store.Issue
	Events  uint64   `json:"events"`
	Users   uint64   `json:"users"`
	Buckets []uint64 `json:"buckets"`
}

type periodDTO struct {
	Name  string    `json:"name"`
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
	Step  int64     `json:"step"` // секунды
}

// period: «24h» — сутки по часам, «14d» — две недели по дням (UTC).
func (a *API) period(name string) (store.Period, periodDTO) {
	now := a.now().UTC()
	var p store.Period
	switch name {
	case "14d":
		until := now.Truncate(24 * time.Hour).Add(24 * time.Hour)
		p = store.Period{Since: until.Add(-14 * 24 * time.Hour), Until: until, Step: 24 * time.Hour}
	default:
		name = "24h"
		until := now.Truncate(time.Hour).Add(time.Hour)
		p = store.Period{Since: until.Add(-24 * time.Hour), Until: until, Step: time.Hour}
	}
	return p, periodDTO{Name: name, Since: p.Since, Until: p.Until, Step: int64(p.Step.Seconds())}
}

func (a *API) listIssues(w http.ResponseWriter, r *http.Request) {
	projectID, ok := a.pathID(w, r, "project")
	if !ok {
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	switch status {
	case "", store.StatusUnresolved:
		status = store.StatusUnresolved
	case "all":
		status = ""
	case store.StatusResolved, store.StatusIgnored:
	default:
		a.fail(w, http.StatusBadRequest, "status: unresolved, resolved, ignored или all")
		return
	}
	period, pdto := a.period(q.Get("period"))

	issues, err := a.Backend.Issues(r.Context(), store.IssueFilter{ProjectID: projectID, Status: status, Limit: 1000})
	if err != nil {
		a.internal(w, err)
		return
	}
	stats, err := a.Backend.IssueStats(r.Context(), projectID, 0, period)
	if err != nil {
		a.internal(w, err)
		return
	}

	needle := strings.ToLower(strings.TrimSpace(q.Get("q")))
	out := make([]issueDTO, 0, len(issues))
	for _, i := range issues {
		if needle != "" && !strings.Contains(strings.ToLower(i.Title+" "+i.Culprit), needle) {
			continue
		}
		st := stats[i.ID]
		if st.Buckets == nil {
			st.Buckets = make([]uint64, period.Buckets())
		}
		out = append(out, issueDTO{Issue: i, Events: st.Events, Users: st.Users, Buckets: st.Buckets})
	}
	switch q.Get("sort") {
	case "events":
		sort.SliceStable(out, func(x, y int) bool { return out[x].Events > out[y].Events })
	case "users":
		sort.SliceStable(out, func(x, y int) bool { return out[x].Users > out[y].Users })
	case "new":
		sort.SliceStable(out, func(x, y int) bool { return out[x].FirstSeen.After(out[y].FirstSeen) })
	}
	writeJSON(w, http.StatusOK, map[string]any{"period": pdto, "issues": out})
}

// tagOrder — в каком порядке показывать теги на странице проблемы.
var tagOrder = []string{"environment", "release", "browser", "os", "runtime", "device", "url", "server_name", "transaction", "level", "user"}

type tagDTO struct {
	Key    string           `json:"key"`
	Total  uint64           `json:"total"`
	Values []store.TagValue `json:"values"`
}

func (a *API) getIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := a.loadIssue(w, r)
	if !ok {
		return
	}
	project, err := a.Backend.Project(r.Context(), issue.ProjectID)
	if err != nil {
		a.internal(w, err)
		return
	}
	resp := map[string]any{"issue": issue, "project": a.projectDTO(project, 0)}
	for _, name := range []string{"24h", "14d"} {
		p, pdto := a.period(name)
		stats, err := a.Backend.IssueStats(r.Context(), issue.ProjectID, issue.ID, p)
		if err != nil {
			a.internal(w, err)
			return
		}
		st := stats[issue.ID]
		if st.Buckets == nil {
			st.Buckets = make([]uint64, p.Buckets())
		}
		resp["stats_"+name] = map[string]any{"period": pdto, "events": st.Events, "users": st.Users, "buckets": st.Buckets}
	}

	values, totals, err := a.Backend.IssueTags(r.Context(), issue.ProjectID, issue.ID, 5)
	if err != nil {
		a.internal(w, err)
		return
	}
	tags := make([]tagDTO, 0, len(values))
	for k, v := range values {
		tags = append(tags, tagDTO{Key: k, Total: totals[k], Values: v})
	}
	rank := func(k string) int {
		for i, t := range tagOrder {
			if t == k {
				return i
			}
		}
		return len(tagOrder)
	}
	sort.Slice(tags, func(x, y int) bool {
		rx, ry := rank(tags[x].Key), rank(tags[y].Key)
		if rx != ry {
			return rx < ry
		}
		return tags[x].Key < tags[y].Key
	})
	resp["tags"] = tags
	writeJSON(w, http.StatusOK, resp)
}

func (a *API) updateIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := a.loadIssue(w, r)
	if !ok {
		return
	}
	var req struct{ Status string }
	if !a.read(w, r, &req) {
		return
	}
	if !store.ValidStatus(req.Status) {
		a.fail(w, http.StatusBadRequest, "status: unresolved, resolved или ignored")
		return
	}
	if err := a.Backend.SetIssueStatus(r.Context(), issue.ID, req.Status); err != nil {
		a.internal(w, err)
		return
	}
	updated, err := a.Backend.Issue(r.Context(), issue.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"issue": updated})
}

func (a *API) listEvents(w http.ResponseWriter, r *http.Request) {
	issue, ok := a.loadIssue(w, r)
	if !ok {
		return
	}
	var before time.Time
	if v := r.URL.Query().Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			a.fail(w, http.StatusBadRequest, "before: время в формате RFC 3339")
			return
		}
		before = t
	}
	const limit = 50
	list, err := a.Backend.IssueEvents(r.Context(), issue.ProjectID, issue.ID, before, limit)
	if err != nil {
		a.internal(w, err)
		return
	}
	resp := map[string]any{"events": list}
	if len(list) == limit {
		resp["next_before"] = list[len(list)-1].Timestamp
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *API) getEvent(w http.ResponseWriter, r *http.Request) {
	issue, ok := a.loadIssue(w, r)
	if !ok {
		return
	}
	id := strings.ToLower(r.PathValue("event"))
	if id != "latest" && !isHex32(id) {
		a.fail(w, http.StatusBadRequest, "id события: 32 шестнадцатеричных символа или latest")
		return
	}
	ev, err := a.Backend.Event(r.Context(), issue.ProjectID, issue.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		a.fail(w, http.StatusNotFound, "событие не найдено: возможно, оно старше срока хранения")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	older, newer, err := a.Backend.Neighbors(r.Context(), issue.ProjectID, issue.ID, ev)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"event": buildEvent(ev), "older": older, "newer": newer})
}

// ---------- общее ----------

func (a *API) loadIssue(w http.ResponseWriter, r *http.Request) (store.Issue, bool) {
	id, ok := a.pathID(w, r, "issue")
	if !ok {
		return store.Issue{}, false
	}
	issue, err := a.Backend.Issue(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		a.fail(w, http.StatusNotFound, "проблема не найдена")
		return store.Issue{}, false
	}
	if err != nil {
		a.internal(w, err)
		return store.Issue{}, false
	}
	return issue, true
}

func (a *API) pathID(w http.ResponseWriter, r *http.Request, name string) (uint64, bool) {
	id, err := strconv.ParseUint(r.PathValue(name), 10, 64)
	if err != nil || id == 0 {
		a.fail(w, http.StatusBadRequest, name+": ждём число")
		return 0, false
	}
	return id, true
}

func (a *API) read(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := dec.Decode(v); err != nil {
		a.fail(w, http.StatusBadRequest, "тело запроса: ждём JSON")
		return false
	}
	return true
}

func (a *API) fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (a *API) internal(w http.ResponseWriter, err error) {
	a.Log.Error("api", "err", err)
	a.fail(w, http.StatusInternalServerError, "внутренняя ошибка, подробности в логе сервера")
}

func (a *API) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func isHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func remoteIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ip, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(ip)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------- защита от подбора пароля ----------

// loginLimiter: после 5 неудачных попыток с одного адреса вход с него
// закрыт на 5 минут.
type loginLimiter struct {
	mu    sync.Mutex
	fails map[string]*attempts
}

type attempts struct {
	n     int
	until time.Time
}

const (
	maxLoginFails = 5
	loginLockout  = 5 * time.Minute
)

func (l *loginLimiter) blocked(ip string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if a := l.fails[ip]; a != nil && a.n >= maxLoginFails && now.Before(a.until) {
		return a.until.Sub(now)
	}
	return 0
}

func (l *loginLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fails == nil || len(l.fails) > 100_000 {
		l.fails = map[string]*attempts{}
	}
	a := l.fails[ip]
	if a == nil || now.After(a.until) {
		a = &attempts{}
		l.fails[ip] = a
	}
	a.n++
	a.until = now.Add(loginLockout)
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
}
