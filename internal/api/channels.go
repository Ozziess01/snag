package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Ozziess01/snag/internal/store"
)

// Notifier — то, что API нужно от уведомлений. nil — Telegram не настроен
// (нет SNAG_TELEGRAM_TOKEN) или это демо в браузере.
type Notifier interface {
	Test(ctx context.Context, ch store.Channel, project store.Project) error
	Forget(projectID uint64)
}

type ChannelStore interface {
	Channels(ctx context.Context, projectID uint64) ([]store.Channel, error)
	SaveChannel(ctx context.Context, c store.Channel) (store.Channel, error)
	DeleteChannel(ctx context.Context, projectID, id uint64) error
}

func (a *API) registerChannels(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/settings", a.authed(a.settings))
	mux.HandleFunc("GET /api/v1/projects/{project}/channels", a.authed(a.listChannels))
	mux.HandleFunc("POST /api/v1/projects/{project}/channels", a.authed(a.saveChannel))
	mux.HandleFunc("PUT /api/v1/projects/{project}/channels/{channel}", a.authed(a.saveChannel))
	mux.HandleFunc("DELETE /api/v1/projects/{project}/channels/{channel}", a.authed(a.deleteChannel))
	mux.HandleFunc("POST /api/v1/projects/{project}/channels/{channel}/test", a.authed(a.testChannel))
}

func (a *API) settings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"telegram": map[string]any{"enabled": a.Notifier != nil, "bot": a.BotName},
	})
}

func (a *API) listChannels(w http.ResponseWriter, r *http.Request) {
	project, ok := a.loadProject(w, r)
	if !ok {
		return
	}
	list, err := a.Backend.Channels(r.Context(), project.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	if list == nil {
		list = []store.Channel{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": list})
}

func (a *API) saveChannel(w http.ResponseWriter, r *http.Request) {
	project, ok := a.loadProject(w, r)
	if !ok {
		return
	}
	var c store.Channel
	if !a.read(w, r, &c) {
		return
	}
	c.ID, c.ProjectID, c.Kind = 0, project.ID, store.KindTelegram
	status := http.StatusCreated
	if r.Method == http.MethodPut {
		id, ok := a.pathID(w, r, "channel")
		if !ok {
			return
		}
		c.ID, status = id, http.StatusOK
	}
	saved, err := a.Backend.SaveChannel(r.Context(), c)
	var bad store.ValidationError
	switch {
	case errors.As(err, &bad):
		a.fail(w, http.StatusBadRequest, bad.Msg)
		return
	case errors.Is(err, store.ErrNotFound):
		a.fail(w, http.StatusNotFound, "канал не найден")
		return
	case errors.Is(err, store.ErrDuplicate):
		a.fail(w, http.StatusConflict, "этот чат уже подключён к проекту")
		return
	case err != nil:
		a.internal(w, err)
		return
	}
	if a.Notifier != nil {
		a.Notifier.Forget(project.ID)
	}
	writeJSON(w, status, map[string]any{"channel": saved})
}

func (a *API) deleteChannel(w http.ResponseWriter, r *http.Request) {
	project, ok := a.loadProject(w, r)
	if !ok {
		return
	}
	id, ok := a.pathID(w, r, "channel")
	if !ok {
		return
	}
	err := a.Backend.DeleteChannel(r.Context(), project.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		a.fail(w, http.StatusNotFound, "канал не найден")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	if a.Notifier != nil {
		a.Notifier.Forget(project.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) testChannel(w http.ResponseWriter, r *http.Request) {
	if a.Notifier == nil {
		a.fail(w, http.StatusServiceUnavailable, "Telegram не настроен на сервере: задайте SNAG_TELEGRAM_TOKEN")
		return
	}
	project, ok := a.loadProject(w, r)
	if !ok {
		return
	}
	id, ok := a.pathID(w, r, "channel")
	if !ok {
		return
	}
	list, err := a.Backend.Channels(r.Context(), project.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, c := range list {
		if c.ID != id {
			continue
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := a.Notifier.Test(ctx, c, project); err != nil {
			// Ответ Telegram понятен человеку («chat not found», «bot was
			// blocked by the user»), его и показываем.
			a.fail(w, http.StatusBadGateway, "Telegram не принял сообщение: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	a.fail(w, http.StatusNotFound, "канал не найден")
}

func (a *API) loadProject(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	id, ok := a.pathID(w, r, "project")
	if !ok {
		return store.Project{}, false
	}
	p, err := a.Backend.Project(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		a.fail(w, http.StatusNotFound, "проект не найден")
		return store.Project{}, false
	}
	if err != nil {
		a.internal(w, err)
		return store.Project{}, false
	}
	return p, true
}
