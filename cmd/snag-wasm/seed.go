//go:build js && wasm

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/Ozziess01/snag/internal/demo"
	"github.com/Ozziess01/snag/internal/store"
	"github.com/Ozziess01/snag/internal/store/mem"
)

// seed заливает историю демо-магазина через настоящий приём, как от SDK.
func seed(h http.Handler, projectID uint64, key string, now time.Time) int {
	envs := demo.Envelopes(now)
	for _, env := range envs {
		r := httptest.NewRequest("POST", fmt.Sprintf("/api/%d/envelope/?sentry_key=%s", projectID, key), strings.NewReader(env))
		r.RemoteAddr = "127.0.0.1:1"
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	return len(envs)
}

func resolveSeeded(ctx context.Context, st *mem.Store, projectID uint64) {
	issues, _ := st.Issues(ctx, store.IssueFilter{ProjectID: projectID})
	for _, i := range issues {
		if strings.HasPrefix(i.Title, demo.ResolvedTitle) {
			_ = st.SetIssueStatus(ctx, i.ID, store.StatusResolved)
		}
	}
}
