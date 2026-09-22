package api

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/Ozziess01/snag/internal/event"
	"github.com/Ozziess01/snag/internal/store"
)

// eventDTO — событие в том виде, в каком его удобно рисовать: исключения
// с кодом вокруг каждой строки стека, теги списком, исходный JSON отдельно.
type eventDTO struct {
	ID          string                     `json:"id"`
	Timestamp   time.Time                  `json:"timestamp"`
	ReceivedAt  time.Time                  `json:"received_at"`
	Level       string                     `json:"level"`
	Platform    string                     `json:"platform"`
	Environment string                     `json:"environment"`
	Release     string                     `json:"release"`
	Title       string                     `json:"title"`
	Culprit     string                     `json:"culprit"`
	Message     string                     `json:"message"`
	SDK         *event.SDK                 `json:"sdk"`
	Exceptions  []exceptionDTO             `json:"exceptions"`
	Stacktrace  []frameDTO                 `json:"stacktrace"` // стек без исключения (captureMessage)
	Breadcrumbs []event.Breadcrumb         `json:"breadcrumbs"`
	Request     *requestDTO                `json:"request"`
	User        *event.User                `json:"user"`
	Contexts    map[string]json.RawMessage `json:"contexts"`
	Extra       map[string]json.RawMessage `json:"extra"`
	Tags        [][2]string                `json:"tags"`
	Fingerprint []string                   `json:"fingerprint"`
	Raw         json.RawMessage            `json:"raw"`
}

type exceptionDTO struct {
	Type      string           `json:"type"`
	Value     string           `json:"value"`
	Module    string           `json:"module"`
	Mechanism *event.Mechanism `json:"mechanism"`
	Frames    []frameDTO       `json:"frames"`
}

type frameDTO struct {
	Filename string       `json:"filename"`
	AbsPath  string       `json:"abs_path"`
	Function string       `json:"function"`
	Module   string       `json:"module"`
	Lineno   int          `json:"lineno"`
	Colno    int          `json:"colno"`
	InApp    bool         `json:"in_app"`
	Context  []sourceLine `json:"context"`
}

type sourceLine struct {
	Line int    `json:"line"`
	Code string `json:"code"`
}

type requestDTO struct {
	URL         string          `json:"url"`
	Method      string          `json:"method"`
	Headers     [][2]string     `json:"headers"`
	QueryString json.RawMessage `json:"query_string"`
	Data        json.RawMessage `json:"data"`
}

func buildEvent(ev store.Event) eventDTO {
	dto := eventDTO{
		ID: ev.EventID, Timestamp: ev.Timestamp, ReceivedAt: ev.ReceivedAt, Level: ev.Level, Platform: ev.Platform,
		Environment: ev.Environment, Release: ev.Release, Title: ev.Title, Culprit: ev.Culprit,
		Raw: json.RawMessage(ev.Data),
	}
	for k, v := range ev.Tags {
		dto.Tags = append(dto.Tags, [2]string{k, v})
	}
	e, err := event.Decode([]byte(ev.Data))
	if err != nil {
		// Сырой JSON не разобрался (такого не должно быть: он прошёл приём),
		// но показать заголовок и теги всё равно можно.
		dto.Raw = nil
		sortTags(dto.Tags)
		return dto
	}
	if len(dto.Tags) == 0 {
		for k, v := range event.DerivedTags(e) {
			dto.Tags = append(dto.Tags, [2]string{k, v})
		}
	}
	sortTags(dto.Tags)

	dto.SDK, dto.User, dto.Contexts, dto.Extra, dto.Fingerprint = e.SDK, e.User, e.Contexts, e.Extra, e.Fingerprint
	dto.Breadcrumbs = e.Breadcrumbs
	if e.LogEntry != nil && e.LogEntry.Text() != "" {
		dto.Message = e.LogEntry.Text()
	} else {
		dto.Message = e.Message.Text()
	}
	for _, ex := range e.Exception {
		x := exceptionDTO{Type: ex.Type, Value: ex.Value, Module: ex.Module, Mechanism: ex.Mechanism}
		if ex.Stacktrace != nil {
			x.Frames = frames(ex.Stacktrace.Frames)
		}
		dto.Exceptions = append(dto.Exceptions, x)
	}
	if len(dto.Exceptions) == 0 {
		switch {
		case e.Stacktrace != nil:
			dto.Stacktrace = frames(e.Stacktrace.Frames)
		default:
			for _, t := range e.Threads {
				if t.Stacktrace != nil && (t.Crashed || t.Current || len(e.Threads) == 1) {
					dto.Stacktrace = frames(t.Stacktrace.Frames)
					break
				}
			}
		}
	}
	if rq := e.Request; rq != nil {
		dto.Request = &requestDTO{URL: rq.URL, Method: rq.Method, Headers: rq.Headers, QueryString: rq.QueryString, Data: rq.Data}
	}
	return dto
}

// frames добавляет номера строк к окружающему коду: SDK присылают
// pre_context и post_context просто списками строк.
func frames(in []event.Frame) []frameDTO {
	out := make([]frameDTO, len(in))
	for i, f := range in {
		d := frameDTO{Filename: f.Filename, AbsPath: f.AbsPath, Function: f.Function, Module: f.Module,
			Lineno: f.Lineno, Colno: f.Colno, InApp: f.InApp != nil && *f.InApp}
		if f.ContextLine != "" && f.Lineno > 0 {
			start := f.Lineno - len(f.PreContext)
			for j, line := range f.PreContext {
				d.Context = append(d.Context, sourceLine{start + j, line})
			}
			d.Context = append(d.Context, sourceLine{f.Lineno, f.ContextLine})
			for j, line := range f.PostContext {
				d.Context = append(d.Context, sourceLine{f.Lineno + 1 + j, line})
			}
		}
		out[i] = d
	}
	return out
}

func sortTags(tags [][2]string) {
	sort.Slice(tags, func(i, j int) bool { return tags[i][0] < tags[j][0] })
}
