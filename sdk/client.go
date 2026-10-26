// Package goruntrace observes explicit function calls without scheduling them.
package goruntrace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Singmin/GorunTrace/pkg/event"
)

type Config struct {
	Endpoint       string
	Service        string
	Token          string
	BufferSize     int
	BatchSize      int
	FlushInterval  time.Duration
	RequestTimeout time.Duration
	CloseTimeout   time.Duration
	MaxRetries     int
	// ErrorFilter runs after default credential redaction. It must be concurrency-safe.
	ErrorFilter func(string) string
}

type Stats struct {
	Queued  uint64 `json:"queued"`
	Sent    uint64 `json:"sent"`
	Dropped uint64 `json:"dropped"`
}
type Client struct {
	cfg      Config
	http     *http.Client
	queue    chan event.Event
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	mu       sync.RWMutex
	closed   bool
	once     sync.Once
	closeErr error
	queued   atomic.Uint64
	sent     atomic.Uint64
	dropped  atomic.Uint64
}

func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("goruntrace: Endpoint must be an HTTP(S) base URL")
	}
	if !event.ValidName(cfg.Service) {
		return nil, errors.New("goruntrace: Service must contain 1–200 bytes")
	}
	if cfg.BufferSize == 0 {
		cfg.BufferSize = 1024
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 100
	}
	if cfg.FlushInterval == 0 {
		cfg.FlushInterval = time.Second
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 2 * time.Second
	}
	if cfg.CloseTimeout == 0 {
		cfg.CloseTimeout = 5 * time.Second
	}
	if cfg.BufferSize < 1 || cfg.BatchSize < 1 || cfg.BatchSize > 500 || cfg.FlushInterval < 0 || cfg.RequestTimeout < 0 || cfg.CloseTimeout < 0 || cfg.MaxRetries < 0 || cfg.MaxRetries > 5 {
		return nil, errors.New("goruntrace: invalid buffer, batch, timeout or retry configuration")
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{cfg: cfg, http: &http.Client{Timeout: cfg.RequestTimeout}, queue: make(chan event.Event, cfg.BufferSize), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go c.worker()
	return c, nil
}

// Trace preserves the function's return value and rethrows the original panic.
// Cancellation is reported only when the returned error wraps the context error.
func (c *Client) Trace(ctx context.Context, taskName string, fn func(context.Context) error) (err error) {
	start := time.Now().UTC()
	runID := event.ID()
	base := event.Event{RunID: runID, Service: c.cfg.Service, TaskName: taskName, Attempt: 1, StartedAt: start}
	valid := event.ValidName(taskName)
	emit := func(kind, summary string) {
		e := base
		e.EventID = event.ID()
		e.Type = kind
		e.OccurredAt = time.Now().UTC()
		e.DurationMS = float64(time.Since(start).Microseconds()) / 1000
		if kind == event.Started {
			e.OccurredAt = start
			e.DurationMS = 0
		}
		e.ErrorSummary = summary
		if valid {
			c.enqueue(e)
		} else {
			c.dropped.Add(1)
		}
	}
	emit(event.Started, "")
	completed := false
	defer func() {
		if !completed {
			p := recover()
			if p != nil {
				emit(event.Panicked, c.filter(fmt.Sprint(p)))
				panic(p)
			}
			// runtime.Goexit runs defers but has no panic to rethrow.
			return
		}
		status, summary := event.Succeeded, ""
		if err != nil {
			status = event.Failed
			summary = c.filter(safeError(err))
			if errors.Is(err, context.DeadlineExceeded) {
				status = event.TimedOut
			} else if errors.Is(err, context.Canceled) {
				status = event.Canceled
			}
		}
		emit(status, summary)
	}()
	err = fn(ctx)
	completed = true
	return err
}

func safeError(err error) (summary string) {
	defer func() {
		if recover() != nil {
			summary = "[error summary unavailable]"
		}
	}()
	return err.Error()
}

var credentials = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer\s+)?|bearer\s+|(?:api[_-]?key|token|password|secret)\s*[:=]\s*)[^\s,;]+`)

func Redact(s string) string { return credentials.ReplaceAllString(s, "${1}[REDACTED]") }
func (c *Client) filter(s string) (out string) {
	// An application-supplied filter must never change task semantics by panicking.
	defer func() {
		if recover() != nil {
			out = "[error filter failed]"
		}
	}()
	out = Redact(s)
	if c.cfg.ErrorFilter != nil {
		out = c.cfg.ErrorFilter(out)
	}
	out = Redact(out)
	if len(out) > 2048 {
		out = strings.ToValidUTF8(out[:2045], "") + "..."
	}
	return out
}
func (c *Client) enqueue(e event.Event) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		c.dropped.Add(1)
		return
	}
	select {
	case c.queue <- e:
		c.queued.Add(1)
	default:
		c.dropped.Add(1)
	}
}
func (c *Client) Stats() Stats { return Stats{c.queued.Load(), c.sent.Load(), c.dropped.Load()} }

// Close stops accepting events and drains the queue within CloseTimeout.
// Calls in progress continue normally; their subsequent events are counted as dropped.
func (c *Client) Close() error {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		close(c.queue)
		c.mu.Unlock()
		t := time.NewTimer(c.cfg.CloseTimeout)
		defer t.Stop()
		select {
		case <-c.done:
		case <-t.C:
			c.cancel()
			<-c.done
			c.closeErr = errors.New("goruntrace: close timeout; unsent events dropped")
		}
		c.cancel()
		c.http.CloseIdleConnections()
	})
	return c.closeErr
}
func (c *Client) worker() {
	defer close(c.done)
	ticker := time.NewTicker(c.cfg.FlushInterval)
	defer ticker.Stop()
	batch := make([]event.Event, 0, c.cfg.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if c.send(batch) {
			c.sent.Add(uint64(len(batch)))
		} else {
			c.dropped.Add(uint64(len(batch)))
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-c.ctx.Done():
			c.dropped.Add(uint64(len(batch)))
			for range c.queue {
				c.dropped.Add(1)
			}
			return
		case e, ok := <-c.queue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, e)
			if len(batch) >= c.cfg.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}
func (c *Client) send(events []event.Event) bool {
	body, err := json.Marshal(event.Batch{Events: events})
	if err != nil {
		return false
	}
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if c.ctx.Err() != nil {
			return false
		}
		req, err := http.NewRequestWithContext(c.ctx, http.MethodPost, c.cfg.Endpoint+"/api/v1/events/batch", bytes.NewReader(body))
		if err != nil {
			return false
		}
		req.Header.Set("Content-Type", "application/json")
		if c.cfg.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
		}
		resp, err := c.http.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return true
			}
			if resp.StatusCode < 500 && resp.StatusCode != 429 {
				return false
			}
		}
		if attempt < c.cfg.MaxRetries {
			t := time.NewTimer(time.Duration(1<<attempt) * 100 * time.Millisecond)
			select {
			case <-c.ctx.Done():
				t.Stop()
				return false
			case <-t.C:
			}
		}
	}
	return false
}
