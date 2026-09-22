package demo

import (
	"strings"
	"testing"
	"time"

	"github.com/Ozziess01/snag/internal/envelope"
	"github.com/Ozziess01/snag/internal/event"
	"github.com/Ozziess01/snag/internal/grouping"
)

func TestEnvelopes(t *testing.T) {
	now := time.Now()
	envs := Envelopes(now)
	if len(envs) != 185 {
		t.Fatalf("событий %d", len(envs))
	}
	groups := map[uint64]string{}
	withCode := 0
	for _, raw := range envs {
		env, err := envelope.Parse([]byte(raw), envelope.Limits{})
		if err != nil || len(env.Items) != 1 {
			t.Fatalf("конверт: %v", err)
		}
		e, err := event.Decode(env.Items[0].Payload)
		if err != nil {
			t.Fatal(err)
		}
		event.Normalize(e, env.Header.EventID, now)
		if e.Timestamp.After(now) || now.Sub(e.Timestamp.Time) > 17*24*time.Hour {
			t.Errorf("время вне двух недель: %v", e.Timestamp)
		}
		groups[grouping.Compute(e).Hash] = e.Title()
		for _, ex := range e.Exception {
			for _, f := range ex.Stacktrace.Frames {
				if f.ContextLine != "" && !strings.HasPrefix(f.ContextLine, "»") {
					withCode++
				}
			}
		}
	}
	if len(groups) != 7 {
		t.Errorf("проблем %d, ждали 7: %v", len(groups), groups)
	}
	if withCode == 0 {
		t.Error("у кадров должен быть код вокруг строки падения")
	}
}
