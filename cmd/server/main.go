package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Singmin/GorunTrace/internal/api"
	"github.com/Singmin/GorunTrace/internal/storage"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	dbPath := flag.String("db", "data/goruntrace.db", "SQLite database path")
	webDir := flag.String("web", "web/dist", "built Dashboard directory")
	flag.Parse()
	token := os.Getenv("GORUNTRACE_TOKEN")
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if token == "" && !(host == "localhost" || (ip != nil && ip.IsLoopback())) {
		return errors.New("GORUNTRACE_TOKEN is required for non-loopback listeners")
	}
	if err = os.MkdirAll(filepath.Dir(*dbPath), 0700); err != nil {
		return err
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	web := http.FileServer(http.Dir(*webDir))
	server := &http.Server{Addr: *addr, Handler: api.New(store, token, web), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ended := make(chan error, 1)
	go func() { ended <- server.ListenAndServe() }()
	slog.Info("GorunTrace listening", "url", "http://"+*addr, "db", *dbPath)
	select {
	case err = <-ended:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}
