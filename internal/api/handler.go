package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Singmin/GorunTrace/internal/storage"
	"github.com/Singmin/GorunTrace/pkg/event"
)

func New(store *storage.Store, token string, web http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /api/v1/events/batch", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var batch event.Batch
		if err := decoder.Decode(&batch); err != nil {
			fail(w, 400, "invalid JSON batch or request larger than 2 MiB")
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			fail(w, 400, "body must contain one JSON object")
			return
		}
		if len(batch.Events) < 1 || len(batch.Events) > 500 {
			fail(w, 400, "batch must contain 1–500 events")
			return
		}
		for _, e := range batch.Events {
			if err := e.Validate(); err != nil {
				fail(w, 400, err.Error())
				return
			}
		}
		n, err := store.Ingest(r.Context(), batch.Events)
		if errors.Is(err, storage.ErrConflict) {
			fail(w, 409, err.Error())
			return
		}
		if err != nil {
			internal(w, err)
			return
		}
		write(w, 202, map[string]int{"accepted": n, "duplicates": len(batch.Events) - n})
	})
	mux.HandleFunc("GET /api/v1/runs", func(w http.ResponseWriter, r *http.Request) {
		f, err := Filter(r)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		v, err := store.Runs(r.Context(), f)
		if err != nil {
			internal(w, err)
			return
		}
		write(w, 200, v)
	})
	mux.HandleFunc("GET /api/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		f, err := Filter(r)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		v, err := store.Tasks(r.Context(), f)
		if err != nil {
			internal(w, err)
			return
		}
		write(w, 200, v)
	})
	mux.HandleFunc("GET /api/v1/services", func(w http.ResponseWriter, r *http.Request) {
		f, err := Filter(r)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		v, err := store.Services(r.Context(), f)
		if err != nil {
			internal(w, err)
			return
		}
		write(w, 200, v)
	})
	mux.HandleFunc("GET /api/v1/metrics/summary", func(w http.ResponseWriter, r *http.Request) {
		f, err := Filter(r)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		v, err := store.Summary(r.Context(), f)
		if err != nil {
			internal(w, err)
			return
		}
		write(w, 200, v)
	})
	mux.HandleFunc("GET /api/v1/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if len(id) > 200 {
			fail(w, 400, "invalid run ID")
			return
		}
		v, err := store.Detail(r.Context(), id)
		if errors.Is(err, storage.ErrNotFound) {
			fail(w, 404, "run not found")
			return
		}
		if err != nil {
			internal(w, err)
			return
		}
		write(w, 200, v)
	})
	if web != nil {
		mux.Handle("GET /", web)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			if token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
				fail(w, 401, "valid bearer token required")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func Filter(r *http.Request) (storage.Filter, error) {
	q := r.URL.Query()
	now := time.Now().UTC()
	f := storage.Filter{Service: q.Get("service"), TaskName: q.Get("task_name"), TaskID: q.Get("task_id"), Status: q.Get("status"), Error: q.Get("error"), From: now.Add(-24 * time.Hour), To: now, Limit: 50, FailuresOnly: q.Get("failures_only") == "true"}
	var err error
	for _, p := range []struct {
		key string
		dst *int
	}{{"limit", &f.Limit}, {"offset", &f.Offset}} {
		if v := q.Get(p.key); v != "" {
			*p.dst, err = strconv.Atoi(v)
			if err != nil {
				return f, errors.New(p.key + " must be an integer")
			}
		}
	}
	for _, p := range []struct {
		key string
		dst *time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := q.Get(p.key); v != "" {
			*p.dst, err = time.Parse(time.RFC3339Nano, v)
			if err != nil {
				return f, errors.New(p.key + " must be an RFC3339 timestamp")
			}
		}
	}
	if q.Get("from") == "" && q.Get("to") != "" {
		f.From = f.To.Add(-24 * time.Hour)
	}
	return f, f.Validate()
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	write(w, status, map[string]string{"error": msg})
}
func internal(w http.ResponseWriter, err error) {
	slog.Error("API storage error", "error", err)
	fail(w, 500, "storage operation failed")
}
