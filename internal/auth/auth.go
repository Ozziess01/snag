// Package auth достаёт публичный ключ проекта из запроса SDK.
//
// SDK передают ключ по-разному:
//   - серверные — заголовком X-Sentry-Auth (изредка Authorization)
//     вида "Sentry sentry_key=…, sentry_version=7, sentry_client=…";
//   - браузерный — параметрами query (?sentry_key=…&sentry_version=7):
//     свой заголовок в браузере вызвал бы CORS preflight на каждый отчёт;
//   - туннели — полем dsn в заголовке конверта.
//
// Ключ публичный (он лежит в коде страницы), поэтому это опознание проекта,
// а не защита. Защищают лимиты и список разрешённых Origin.
package auth

import (
	"net/http"
	"net/url"
	"strings"
)

type Credentials struct {
	Key     string
	Version string
	Client  string
}

// FromRequest ищет ключ в заголовках, потом в query.
func FromRequest(r *http.Request) (Credentials, bool) {
	for _, h := range []string{"X-Sentry-Auth", "Authorization"} {
		if c, ok := ParseHeader(r.Header.Get(h)); ok {
			return c, true
		}
	}
	q := r.URL.Query()
	c := Credentials{Key: q.Get("sentry_key"), Version: q.Get("sentry_version"), Client: q.Get("sentry_client")}
	return c, c.Key != ""
}

// ParseHeader разбирает "Sentry sentry_key=abc, sentry_version=7".
func ParseHeader(v string) (Credentials, bool) {
	v = strings.TrimSpace(v)
	scheme, rest, ok := strings.Cut(v, " ")
	if !ok || !strings.EqualFold(scheme, "Sentry") {
		return Credentials{}, false
	}
	var c Credentials
	for _, part := range strings.Split(rest, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"`)
		switch strings.TrimSpace(k) {
		case "sentry_key":
			c.Key = val
		case "sentry_version":
			c.Version = val
		case "sentry_client":
			c.Client = val
		}
	}
	return c, c.Key != ""
}

// ParseDSN возвращает ключ и id проекта из DSN
// https://<key>[:<secret>]@<host>[:port]/[path/]<project_id>.
func ParseDSN(dsn string) (key, projectID string, ok bool) {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil || u.User.Username() == "" {
		return "", "", false
	}
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return "", "", false
	}
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		path = path[i+1:]
	}
	return u.User.Username(), path, true
}
