package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Singmin/GorunTrace/pkg/event"
)

func events(run, service, status string, at time.Time, duration float64) []event.Event {
	s := event.Event{EventID: run + "-start", RunID: run, Service: service, TaskName: "sync-orders", Type: event.Started, Attempt: 1, StartedAt: at, OccurredAt: at}
	e := s
	e.EventID = run + "-end"
	e.Type = status
	e.OccurredAt = at.Add(time.Duration(duration * float64(time.Millisecond)))
	e.DurationMS = duration
	if status != event.Succeeded {
		e.ErrorSummary = "upstream unavailable"
	}
	return []event.Event{s, e}
}
func filter() Filter {
	now := time.Now().UTC()
	return Filter{From: now.Add(-24 * time.Hour), To: now, Limit: 50}
}
func TestPersistenceIdempotencyAndOutOfOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	batch := events("one", "orders", event.Failed, time.Now().Add(-time.Minute), 100)
	if n, err := s.Ingest(ctx, []event.Event{batch[1], batch[0]}); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if n, err := s.Ingest(ctx, batch); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d, err := s.Detail(ctx, "one")
	if err != nil || d.Run.Status != event.Failed || len(d.Events) != 2 {
		t.Fatal(d, err)
	}
	conflict := batch[0]
	conflict.Service = "other"
	if _, err = s.Ingest(ctx, []event.Event{conflict}); !errors.Is(err, ErrConflict) {
		t.Fatal("event ID collision accepted", err)
	}
	other := batch[1]
	other.EventID = "another-terminal"
	other.Type = event.Succeeded
	if _, err = s.Ingest(ctx, []event.Event{other}); !errors.Is(err, ErrConflict) {
		t.Fatal("terminal mutation accepted", err)
	}
}
func TestAtomicBatchAndFilteringMetrics(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	at := time.Now().Add(-time.Hour)
	for i, v := range []struct {
		id, service, status string
		ms                  float64
	}{{"a", "orders", event.Succeeded, 100}, {"b", "orders", event.TimedOut, 300}, {"c", "mail", event.Canceled, 200}} {
		if _, err = s.Ingest(ctx, events(v.id, v.service, v.status, at.Add(time.Duration(i)*time.Second), v.ms)); err != nil {
			t.Fatal(err)
		}
	}
	active := events("d", "orders", event.Succeeded, at.Add(time.Minute), 0)[0]
	_, _ = s.Ingest(ctx, []event.Event{active})
	f := filter()
	f.Service = "orders"
	summary, err := s.Summary(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 3 || summary.Failed != 1 || summary.Running != 1 || summary.SuccessRate != 50 || summary.AvgDurationMS != 200 || summary.P95DurationMS != 300 {
		t.Fatalf("bad summary %+v", summary)
	}
	f.Limit = 1
	p, err := s.Runs(ctx, f)
	if err != nil || p.Total != 3 || len(p.Items) != 1 {
		t.Fatal(p, err)
	}
	f.Offset = 1
	p2, _ := s.Runs(ctx, f)
	if p.Items[0].RunID == p2.Items[0].RunID {
		t.Fatal("pagination repeated run")
	}
	f.Offset = 0
	f.FailuresOnly = true
	p, err = s.Runs(ctx, f)
	if err != nil || p.Total != 1 || p.Items[0].Status != event.TimedOut {
		t.Fatal(p, err)
	}
	x := events("new", "orders", event.Succeeded, at, 10)
	conflict := events("a", "wrong", event.Succeeded, at, 10)[0]
	conflict.EventID = "conflicting-run"
	if _, err = s.Ingest(ctx, append(x, conflict)); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.Detail(ctx, "new"); !errors.Is(err, ErrNotFound) {
		t.Fatal("partial batch committed", err)
	}
}
func BenchmarkIngest(b *testing.B) {
	s, err := Open(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := event.ID()
		batch := events(id, "bench", event.Succeeded, time.Now().Add(-time.Second), 1)
		if _, err = s.Ingest(context.Background(), batch); err != nil {
			b.Fatal(err)
		}
	}
}
