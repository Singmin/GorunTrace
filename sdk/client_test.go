package goruntrace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Singmin/GorunTrace/pkg/event"
)

func TestTraceSemanticsAndRedaction(t *testing.T) {
	var mu sync.Mutex
	var events []event.Event
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b event.Batch
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Error(err)
		}
		mu.Lock()
		events = append(events, b.Events...)
		mu.Unlock()
		w.WriteHeader(202)
	}))
	defer server.Close()
	c, err := New(Config{Endpoint: server.URL, Service: "test", BatchSize: 500})
	if err != nil {
		t.Fatal(err)
	}
	root := errors.New("password=hunter2 token=private")
	for _, want := range []error{nil, root, context.DeadlineExceeded, context.Canceled} {
		got := c.Trace(context.Background(), "work", func(context.Context) error { return want })
		if got != want {
			t.Fatal("business error changed")
		}
	}
	marker := &struct{ X int }{42}
	func() {
		defer func() {
			if recover() != marker {
				t.Error("panic identity changed")
			}
		}()
		_ = c.Trace(context.Background(), "panic", func(context.Context) error { panic(marker) })
	}()
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 10 {
		t.Fatalf("got %d events", len(events))
	}
	want := []string{event.Succeeded, event.Failed, event.TimedOut, event.Canceled, event.Panicked}
	for i, status := range want {
		if events[i*2].Type != event.Started || events[i*2+1].Type != status {
			t.Fatalf("wrong transition: %v", events[i*2:i*2+2])
		}
		if err := events[i*2+1].Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(events[3].ErrorSummary, "hunter2") || strings.Contains(events[3].ErrorSummary, "private") {
		t.Fatal("credentials leaked")
	}
	if c.Stats().Sent != 10 || c.Stats().Dropped != 0 {
		t.Fatal(c.Stats())
	}
}
func TestFilterPanicCannotChangeBusinessResult(t *testing.T) {
	c, _ := New(Config{Endpoint: "http://127.0.0.1:1", Service: "test", ErrorFilter: func(string) string { panic("filter broke") }, RequestTimeout: 10 * time.Millisecond})
	want := errors.New("business failed")
	if got := c.Trace(context.Background(), "work", func(context.Context) error { return want }); got != want {
		t.Fatal(got)
	}
	_ = c.Close()
}

type brokenError struct{}

func (brokenError) Error() string { panic("broken Error method") }
func TestErrorStringPanicCannotChangeBusinessResult(t *testing.T) {
	c, _ := New(Config{Endpoint: "http://127.0.0.1:1", Service: "test", RequestTimeout: time.Millisecond})
	want := brokenError{}
	if got := c.Trace(context.Background(), "work", func(context.Context) error { return want }); got != want {
		t.Fatal("business error changed")
	}
	_ = c.Close()
}
func TestQueueDoesNotBlockAndCloseIsBounded(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		once.Do(func() { close(entered) })
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	c, _ := New(Config{Endpoint: server.URL, Service: "test", BufferSize: 2, BatchSize: 1, CloseTimeout: 60 * time.Millisecond, RequestTimeout: time.Second})
	_ = c.Trace(context.Background(), "work", func(context.Context) error { return nil })
	<-entered
	start := time.Now()
	for i := 0; i < 1000; i++ {
		_ = c.Trace(context.Background(), "work", func(context.Context) error { return nil })
	}
	if time.Since(start) > time.Second {
		t.Fatal("queue blocked")
	}
	start = time.Now()
	if c.Close() == nil {
		t.Fatal("expected close deadline")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("unbounded Close")
	}
	stats := c.Stats()
	if stats.Queued+stats.Dropped < 2002 || stats.Sent+stats.Dropped != 2002 {
		t.Fatalf("events not accounted for: %+v", stats)
	}
	_ = c.Trace(context.Background(), "after-close", func(context.Context) error { return nil })
	if c.Stats().Dropped != stats.Dropped+2 {
		t.Fatal("events after close uncounted")
	}
}
func TestRetryKeepsEventIDs(t *testing.T) {
	var attempts int
	var original string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b event.Batch
		_ = json.NewDecoder(r.Body).Decode(&b)
		attempts++
		if attempts == 1 {
			original = b.Events[0].EventID
			w.WriteHeader(503)
			return
		}
		if b.Events[0].EventID != original {
			t.Error("retry changed event ID")
		}
		w.WriteHeader(202)
	}))
	defer server.Close()
	c, _ := New(Config{Endpoint: server.URL, Service: "test", MaxRetries: 1})
	_ = c.Trace(context.Background(), "work", func(context.Context) error { return nil })
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || c.Stats().Sent != 2 {
		t.Fatal(attempts, c.Stats())
	}
}
func TestConcurrentTraceAndClose(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202) }))
	defer s.Close()
	c, _ := New(Config{Endpoint: s.URL, Service: "test"})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = c.Trace(context.Background(), "work", func(context.Context) error { return nil })
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = c.Close() }()
	wg.Wait()
	stats := c.Stats()
	if stats.Sent+stats.Dropped != 2000 {
		t.Fatal(stats)
	}
}
func BenchmarkTrace(b *testing.B) {
	c, _ := New(Config{Endpoint: "http://127.0.0.1:1", Service: "benchmark", BufferSize: 1024, RequestTimeout: time.Millisecond})
	defer c.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Trace(context.Background(), "noop", func(context.Context) error { return nil })
	}
}
