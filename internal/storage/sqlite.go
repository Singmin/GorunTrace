package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Singmin/GorunTrace/pkg/event"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("run not found")
var ErrConflict = errors.New("event conflicts with stored run")

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS services(name TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS tasks(id TEXT PRIMARY KEY, service TEXT NOT NULL REFERENCES services(name), name TEXT NOT NULL, UNIQUE(service,name));
CREATE TABLE IF NOT EXISTS task_runs(run_id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), service TEXT NOT NULL, task_name TEXT NOT NULL, status TEXT NOT NULL, attempt INTEGER NOT NULL, started_at INTEGER NOT NULL, ended_at INTEGER, duration_ms REAL NOT NULL DEFAULT 0, error_summary TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS task_events(event_id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES task_runs(run_id), occurred_at INTEGER NOT NULL, payload TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_runs_started ON task_runs(started_at DESC,run_id);
CREATE INDEX IF NOT EXISTS idx_runs_task ON task_runs(task_id,started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_service_status ON task_runs(service,status,started_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_run ON task_events(run_id,occurred_at);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func TaskID(service, name string) string {
	h := sha256.Sum256([]byte(service + "\x00" + name))
	return hex.EncodeToString(h[:16])
}
func (s *Store) Ingest(ctx context.Context, events []event.Event) (int, error) {
	if len(events) < 1 || len(events) > 500 {
		return 0, errors.New("batch must contain 1–500 events")
	}
	for _, e := range events {
		if err := e.Validate(); err != nil {
			return 0, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	inserted := 0
	for _, e := range events {
		// Normalize timestamps so equivalent offsets are idempotent on the wire.
		e.StartedAt = e.StartedAt.UTC()
		e.OccurredAt = e.OccurredAt.UTC()
		payload, _ := json.Marshal(e)
		var oldPayload string
		err = tx.QueryRowContext(ctx, "SELECT payload FROM task_events WHERE event_id=?", e.EventID).Scan(&oldPayload)
		if err == nil {
			if oldPayload != string(payload) {
				return 0, ErrConflict
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		var service, name, status string
		var start int64
		var attempt int
		err = tx.QueryRowContext(ctx, "SELECT service,task_name,status,started_at,attempt FROM task_runs WHERE run_id=?", e.RunID).Scan(&service, &name, &status, &start, &attempt)
		if err == nil {
			if service != e.Service || name != e.TaskName || start != e.StartedAt.UnixMicro() || attempt != e.Attempt || (status != event.Started && e.Type != event.Started) {
				return 0, ErrConflict
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		id := TaskID(e.Service, e.TaskName)
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO services(name) VALUES(?)", e.Service); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO tasks(id,service,name) VALUES(?,?,?)", id, e.Service, e.TaskName); err != nil {
			return 0, err
		}
		var end any
		if e.Type != event.Started {
			end = e.OccurredAt.UnixMicro()
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_runs(run_id,task_id,service,task_name,status,attempt,started_at,ended_at,duration_ms,error_summary) VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(run_id) DO UPDATE SET status=excluded.status,ended_at=excluded.ended_at,duration_ms=excluded.duration_ms,error_summary=excluded.error_summary WHERE task_runs.status='started' AND excluded.status!='started'`, e.RunID, id, e.Service, e.TaskName, e.Type, e.Attempt, e.StartedAt.UnixMicro(), end, e.DurationMS, e.ErrorSummary)
		if err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO task_events(event_id,run_id,occurred_at,payload) VALUES(?,?,?,?)", e.EventID, e.RunID, e.OccurredAt.UnixMicro(), string(payload)); err != nil {
			return 0, err
		}
		inserted++
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

type Filter struct {
	Service      string    `json:"service,omitempty"`
	TaskName     string    `json:"task_name,omitempty"`
	TaskID       string    `json:"task_id,omitempty"`
	Status       string    `json:"status,omitempty"`
	Error        string    `json:"error,omitempty"`
	From         time.Time `json:"from"`
	To           time.Time `json:"to"`
	Limit        int       `json:"limit"`
	Offset       int       `json:"offset"`
	FailuresOnly bool      `json:"-"`
}

func (f Filter) where() (string, []any) {
	clauses := []string{"started_at>=?", "started_at<=?"}
	args := []any{f.From.UnixMicro(), f.To.UnixMicro()}
	for _, p := range []struct{ key, value string }{{"service", f.Service}, {"task_name", f.TaskName}, {"task_id", f.TaskID}, {"status", f.Status}} {
		if p.value != "" {
			clauses = append(clauses, p.key+"=?")
			args = append(args, p.value)
		}
	}
	if f.Error != "" {
		clauses = append(clauses, "instr(lower(error_summary),lower(?))>0")
		args = append(args, f.Error)
	}
	if f.FailuresOnly {
		clauses = append(clauses, "status IN ('failed','timed_out','panicked')")
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

type Run struct {
	RunID        string     `json:"run_id"`
	TaskID       string     `json:"task_id"`
	Service      string     `json:"service"`
	TaskName     string     `json:"task_name"`
	Status       string     `json:"status"`
	Attempt      int        `json:"attempt"`
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at"`
	DurationMS   float64    `json:"duration_ms"`
	ErrorSummary string     `json:"error_summary"`
}

const runColumns = "run_id,task_id,service,task_name,status,attempt,started_at,ended_at,duration_ms,error_summary"

type scanner interface{ Scan(...any) error }

func scanRun(row scanner) (Run, error) {
	var r Run
	var start int64
	var end sql.NullInt64
	err := row.Scan(&r.RunID, &r.TaskID, &r.Service, &r.TaskName, &r.Status, &r.Attempt, &start, &end, &r.DurationMS, &r.ErrorSummary)
	r.StartedAt = time.UnixMicro(start).UTC()
	if end.Valid {
		t := time.UnixMicro(end.Int64).UTC()
		r.EndedAt = &t
	}
	return r, err
}

type Page[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

func (s *Store) Runs(ctx context.Context, f Filter) (Page[Run], error) {
	page := Page[Run]{Items: []Run{}, Limit: f.Limit, Offset: f.Offset}
	where, args := f.where()
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM task_runs"+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+runColumns+" FROM task_runs"+where+" ORDER BY started_at DESC,run_id LIMIT ? OFFSET ?", append(args, f.Limit, f.Offset)...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, r)
	}
	return page, rows.Err()
}

type Detail struct {
	Run    Run           `json:"run"`
	Events []event.Event `json:"events"`
}

func (s *Store) Detail(ctx context.Context, id string) (Detail, error) {
	d := Detail{Events: []event.Event{}}
	r, err := scanRun(s.db.QueryRowContext(ctx, "SELECT "+runColumns+" FROM task_runs WHERE run_id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	d.Run = r
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM task_events WHERE run_id=? ORDER BY occurred_at,event_id LIMIT 100", id)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var e event.Event
		if err = rows.Scan(&raw); err != nil {
			return d, err
		}
		if err = json.Unmarshal([]byte(raw), &e); err != nil {
			return d, err
		}
		d.Events = append(d.Events, e)
	}
	return d, rows.Err()
}

type Task struct {
	ID       string    `json:"id"`
	Service  string    `json:"service"`
	Name     string    `json:"name"`
	Runs     int       `json:"runs"`
	Failures int       `json:"failures"`
	LastSeen time.Time `json:"last_seen"`
}

func (s *Store) Tasks(ctx context.Context, f Filter) (Page[Task], error) {
	page := Page[Task]{Items: []Task{}, Limit: f.Limit, Offset: f.Offset}
	where, args := f.where()
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT task_id) FROM task_runs"+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT task_id,service,task_name,COUNT(*),SUM(CASE WHEN status IN ('failed','timed_out','panicked') THEN 1 ELSE 0 END),MAX(started_at) FROM task_runs`+where+" GROUP BY task_id ORDER BY MAX(started_at) DESC,task_id LIMIT ? OFFSET ?", append(args, f.Limit, f.Offset)...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var t Task
		var last int64
		if err = rows.Scan(&t.ID, &t.Service, &t.Name, &t.Runs, &t.Failures, &last); err != nil {
			return page, err
		}
		t.LastSeen = time.UnixMicro(last).UTC()
		page.Items = append(page.Items, t)
	}
	return page, rows.Err()
}
func (s *Store) Services(ctx context.Context, f Filter) (Page[string], error) {
	p := Page[string]{Items: []string{}, Limit: f.Limit, Offset: f.Offset}
	where, args := f.where()
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT service) FROM task_runs"+where, args...).Scan(&p.Total); err != nil {
		return p, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT service FROM task_runs"+where+" ORDER BY service LIMIT ? OFFSET ?", append(args, f.Limit, f.Offset)...)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			return p, err
		}
		p.Items = append(p.Items, n)
	}
	return p, rows.Err()
}

type Bucket struct {
	Hour     time.Time `json:"hour"`
	Runs     int       `json:"runs"`
	Failures int       `json:"failures"`
}
type ErrorGroup struct {
	Summary      string `json:"summary"`
	Count        int    `json:"count"`
	ExampleRunID string `json:"example_run_id"`
}
type Summary struct {
	Total         int          `json:"total"`
	Succeeded     int          `json:"succeeded"`
	Failed        int          `json:"failed"`
	Running       int          `json:"running"`
	Canceled      int          `json:"canceled"`
	SuccessRate   float64      `json:"success_rate"`
	AvgDurationMS float64      `json:"avg_duration_ms"`
	P95DurationMS float64      `json:"p95_duration_ms"`
	Buckets       []Bucket     `json:"buckets"`
	Errors        []ErrorGroup `json:"errors"`
	Window        Filter       `json:"window"`
}

func (s *Store) Summary(ctx context.Context, f Filter) (Summary, error) {
	out := Summary{Buckets: []Bucket{}, Errors: []ErrorGroup{}, Window: f}
	where, args := f.where()
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(status='succeeded'),0),COALESCE(SUM(status IN ('failed','timed_out','panicked')),0),COALESCE(SUM(status='started'),0),COALESCE(SUM(status='canceled'),0),COALESCE(AVG(CASE WHEN status!='started' THEN duration_ms END),0) FROM task_runs`+where, args...).Scan(&out.Total, &out.Succeeded, &out.Failed, &out.Running, &out.Canceled, &out.AvgDurationMS)
	if err != nil {
		return out, err
	}
	completed := out.Total - out.Running
	if completed > 0 {
		out.SuccessRate = float64(out.Succeeded) / float64(completed) * 100
		offset := (completed*95+99)/100 - 1
		err = s.db.QueryRowContext(ctx, "SELECT duration_ms FROM task_runs"+where+" AND status!='started' ORDER BY duration_ms LIMIT 1 OFFSET ?", append(args, offset)...).Scan(&out.P95DurationMS)
		if err != nil {
			return out, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT (started_at/3600000000)*3600,COUNT(*),SUM(status IN ('failed','timed_out','panicked')) FROM task_runs`+where+" GROUP BY 1 ORDER BY 1", args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var b Bucket
		var hour int64
		if err = rows.Scan(&hour, &b.Runs, &b.Failures); err != nil {
			rows.Close()
			return out, err
		}
		b.Hour = time.Unix(hour, 0).UTC()
		out.Buckets = append(out.Buckets, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT error_summary,COUNT(*),MIN(run_id) FROM task_runs`+where+" AND status IN ('failed','timed_out','panicked') GROUP BY error_summary ORDER BY COUNT(*) DESC,error_summary LIMIT 5", args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var g ErrorGroup
		if err = rows.Scan(&g.Summary, &g.Count, &g.ExampleRunID); err != nil {
			return out, err
		}
		out.Errors = append(out.Errors, g)
	}
	return out, rows.Err()
}

func (f Filter) Validate() error {
	if f.Limit < 1 || f.Limit > 200 || f.Offset < 0 || f.Offset > 1000000 {
		return errors.New("limit must be 1–200 and offset 0–1000000")
	}
	if f.From.IsZero() || f.To.IsZero() || f.To.Before(f.From) || f.To.Sub(f.From) > 31*24*time.Hour {
		return errors.New("time window must be ordered and at most 31 days")
	}
	if f.Status != "" && !event.ValidStatus(f.Status) {
		return fmt.Errorf("invalid status %q", f.Status)
	}
	for _, v := range []string{f.Service, f.TaskName, f.TaskID, f.Error} {
		if len(v) > 200 {
			return errors.New("filter values must be at most 200 bytes")
		}
	}
	return nil
}
