// Package ingest — HTTP-приём событий от SDK Sentry.
//
// Точки входа те же, что у Sentry, поэтому SDK подключается сменой DSN:
//
//	POST /api/<project_id>/envelope/  — конверт, так шлют все современные SDK;
//	POST /api/<project_id>/store/     — одно событие JSON, старые SDK.
//
// Обработчик только разбирает и проверяет запрос и отдаёт события дальше
// в Sink. Всё тяжёлое (группировка, запись в базу) происходит не здесь,
// чтобы ответ SDK уходил быстро.
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Ozziess01/snag/internal/auth"
	"github.com/Ozziess01/snag/internal/envelope"
	"github.com/Ozziess01/snag/internal/event"
)

// Project — то, что приёму нужно знать о проекте.
type Project struct {
	ID uint64
	// AllowedOrigins — откуда браузерам можно слать события. Пусто — отовсюду.
	AllowedOrigins []string
}

// Projects ищет проект по публичному ключу DSN. Неизвестный ключ —
// ErrUnknownKey; любая другая ошибка значит, что хранилище недоступно.
type Projects interface {
	ByKey(ctx context.Context, key string) (Project, error)
}

var ErrUnknownKey = errors.New("ingest: неизвестный ключ")

// Accepted — событие, прошедшее проверку.
type Accepted struct {
	ProjectID  uint64
	Event      *event.Event
	Raw        []byte // исходный JSON события
	ReceivedAt time.Time
	ClientIP   string
}

// Sink принимает события одного запроса. Пачка принимается целиком или
// не принимается вовсе: SDK повторяет запрос полностью, и половина
// принятого конверта превратилась бы в дубли. ErrBusy означает «очередь
// полна»: SDK получит 429 и повторит позже.
type Sink interface {
	Accept(ctx context.Context, batch []Accepted) error
}

var ErrBusy = errors.New("ingest: очередь переполнена")

type Handler struct {
	Projects Projects
	Sink     Sink
	Log      *slog.Logger
	Now      func() time.Time

	// MaxBodySize — предел тела запроса как пришло по сети (сжатого).
	MaxBodySize int64
	// MaxDecodedSize — предел после распаковки: защита от «zip-бомбы».
	MaxDecodedSize int64
	// MaxEventSize — предел одного события.
	MaxEventSize int
}

func (h *Handler) Register(mux *http.ServeMux) {
	for _, p := range []string{"/api/{project}/envelope/", "/api/{project}/envelope"} {
		mux.HandleFunc("POST "+p, h.envelope)
		mux.HandleFunc("OPTIONS "+p, h.preflight)
	}
	for _, p := range []string{"/api/{project}/store/", "/api/{project}/store"} {
		mux.HandleFunc("POST "+p, h.store)
		mux.HandleFunc("OPTIONS "+p, h.preflight)
	}
}

// ---------- обработчики ----------

func (h *Handler) envelope(w http.ResponseWriter, r *http.Request) {
	h.cors(w, r)
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	env, err := envelope.Parse(body, envelope.Limits{MaxItems: 100, MaxItemSize: h.MaxEventSize})
	if err != nil {
		h.fail(w, http.StatusBadRequest, err.Error())
		return
	}

	// Браузерные туннели кладут DSN прямо в конверт.
	creds, _ := auth.FromRequest(r)
	if creds.Key == "" && env.Header.DSN != "" {
		creds.Key, _, _ = auth.ParseDSN(env.Header.DSN)
	}
	project, ok := h.authorize(w, r, creds.Key)
	if !ok {
		return
	}

	// Сначала разбираем все события, потом отдаём: битый конверт не должен
	// оставить после себя половину принятых событий.
	eventID := env.Header.EventID
	received := h.now()
	var batch []Accepted
	for _, item := range env.Items {
		if item.Header.Type != envelope.TypeEvent {
			// transaction, session, client_report и прочее пока не храним,
			// но принимаем: иначе SDK будет считать это ошибкой и повторять.
			continue
		}
		e, err := event.Decode(item.Payload)
		if err != nil {
			h.fail(w, http.StatusBadRequest, "событие: "+err.Error())
			return
		}
		event.Normalize(e, env.Header.EventID, received)
		eventID = e.EventID
		batch = append(batch, Accepted{ProjectID: project.ID, Event: e, Raw: item.Payload, ReceivedAt: received, ClientIP: clientIP(r)})
	}
	if len(batch) > 0 && !h.accept(w, r, batch) {
		return
	}
	writeID(w, eventID)
}

func (h *Handler) store(w http.ResponseWriter, r *http.Request) {
	h.cors(w, r)
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	if h.MaxEventSize > 0 && len(body) > h.MaxEventSize {
		h.fail(w, http.StatusRequestEntityTooLarge, "событие больше допустимого размера")
		return
	}
	creds, _ := auth.FromRequest(r)
	project, ok := h.authorize(w, r, creds.Key)
	if !ok {
		return
	}
	e, err := event.Decode(body)
	if err != nil {
		h.fail(w, http.StatusBadRequest, "событие: "+err.Error())
		return
	}
	received := h.now()
	event.Normalize(e, "", received)
	if !h.accept(w, r, []Accepted{{ProjectID: project.ID, Event: e, Raw: body, ReceivedAt: received, ClientIP: clientIP(r)}}) {
		return
	}
	writeID(w, e.EventID)
}

func (h *Handler) preflight(w http.ResponseWriter, r *http.Request) {
	h.cors(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// ---------- общие шаги ----------

// readBody читает тело с учётом Content-Encoding и обоих пределов размера.
func (h *Handler) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw := http.MaxBytesReader(w, r.Body, h.MaxBodySize)
	dec, err := decompress(r.Header.Get("Content-Encoding"), raw)
	if err != nil {
		h.fail(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	defer dec.Close()

	// Читаем на байт больше предела, чтобы отличить «ровно предел» от «больше».
	body, err := io.ReadAll(io.LimitReader(dec, h.MaxDecodedSize+1))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		h.fail(w, http.StatusRequestEntityTooLarge, "тело запроса больше допустимого размера")
		return nil, false
	case err != nil:
		h.fail(w, http.StatusBadRequest, "не удалось прочитать тело: "+err.Error())
		return nil, false
	case int64(len(body)) > h.MaxDecodedSize:
		h.fail(w, http.StatusRequestEntityTooLarge, "распакованное тело больше допустимого размера")
		return nil, false
	}
	return body, true
}

// authorize проверяет ключ, совпадение проекта в пути и Origin браузера.
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, key string) (Project, bool) {
	if key == "" {
		h.fail(w, http.StatusUnauthorized, "нет ключа: ждём X-Sentry-Auth, sentry_key в query или dsn в конверте")
		return Project{}, false
	}
	project, err := h.Projects.ByKey(r.Context(), key)
	switch {
	case errors.Is(err, ErrUnknownKey):
		h.fail(w, http.StatusUnauthorized, "неизвестный ключ")
		return Project{}, false
	case err != nil:
		// 401 SDK считает окончательным отказом и выбрасывает событие,
		// а 503 — временной проблемой и повторяет позже.
		h.Log.Error("поиск проекта", "err", err)
		h.fail(w, http.StatusServiceUnavailable, "хранилище проектов недоступно")
		return Project{}, false
	}
	if r.PathValue("project") != fmt.Sprint(project.ID) {
		h.fail(w, http.StatusForbidden, "ключ от другого проекта")
		return Project{}, false
	}
	if origin := r.Header.Get("Origin"); origin != "" && !originAllowed(origin, project.AllowedOrigins) {
		h.fail(w, http.StatusForbidden, "Origin не разрешён для проекта")
		return Project{}, false
	}
	return project, true
}

func (h *Handler) accept(w http.ResponseWriter, r *http.Request, batch []Accepted) bool {
	err := h.Sink.Accept(r.Context(), batch)
	switch {
	case err == nil:
		return true
	case errors.Is(err, ErrBusy):
		// SDK Sentry понимают оба заголовка и не шлют события до конца паузы.
		w.Header().Set("Retry-After", "5")
		w.Header().Set("X-Sentry-Rate-Limits", "5::organization")
		h.fail(w, http.StatusTooManyRequests, "сервер перегружен, повторите позже")
	default:
		h.Log.Error("sink", "err", err, "project", batch[0].ProjectID)
		h.fail(w, http.StatusServiceUnavailable, "не удалось принять событие")
	}
	return false
}

// cors отвечает браузерному SDK. Content-Type у него text/plain, поэтому
// обычный отчёт идёт без preflight; OPTIONS нужен редко, но пусть работает.
func (h *Handler) cors(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	hd := w.Header()
	hd.Set("Access-Control-Allow-Origin", origin)
	hd.Add("Vary", "Origin")
	hd.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	hd.Set("Access-Control-Allow-Headers", "Content-Type, Content-Encoding, X-Sentry-Auth, Authorization, sentry-trace, baggage")
	hd.Set("Access-Control-Expose-Headers", "X-Sentry-Rate-Limits, Retry-After")
	hd.Set("Access-Control-Max-Age", "3600")
}

func (h *Handler) fail(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"detail": detail})
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func writeID(w http.ResponseWriter, id string) {
	w.Header().Set("Content-Type", "application/json")
	if id == "" {
		_, _ = w.Write([]byte("{}"))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
}

func originAllowed(origin string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if a == "*" || strings.EqualFold(a, origin) {
			return true
		}
	}
	return false
}

// clientIP — адрес клиента для user.ip_address. X-Forwarded-For берём
// первым, потому что сервис будет стоять за nginx/Caddy.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ip, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(ip)
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}
