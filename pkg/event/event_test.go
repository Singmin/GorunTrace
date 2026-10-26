package event

import (
	"math"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	now := time.Now().UTC()
	good := Event{EventID: "event", RunID: "run", Service: "orders", TaskName: "sync", Type: Succeeded, Attempt: 1, StartedAt: now.Add(-time.Second), OccurredAt: now, DurationMS: 1000}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Event){"unknown status": func(e *Event) { e.Type = "unknown" }, "control character": func(e *Event) { e.Service = "bad\x00name" }, "empty ID": func(e *Event) { e.EventID = " " }, "negative duration": func(e *Event) { e.DurationMS = -1 }, "NaN": func(e *Event) { e.DurationMS = math.NaN() }, "Inf": func(e *Event) { e.DurationMS = math.Inf(1) }, "timestamp order": func(e *Event) { e.OccurredAt = e.StartedAt.Add(-time.Second) }, "future timestamp": func(e *Event) { e.OccurredAt = now.Add(time.Hour) }, "bad attempt": func(e *Event) { e.Attempt = 0 }, "start with result": func(e *Event) { e.Type = Started }} {
		t.Run(name, func(t *testing.T) {
			e := good
			mutate(&e)
			if e.Validate() == nil {
				t.Fatal("invalid event accepted")
			}
		})
	}
}
