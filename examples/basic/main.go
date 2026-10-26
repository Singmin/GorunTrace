// The example sends real executions of intentionally simulated business tasks.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	goruntrace "github.com/Singmin/GorunTrace/sdk"
)

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:8080", "collector endpoint")
	count := flag.Int("count", 24, "number of example executions (1–1000)")
	flag.Parse()
	if *count < 1 || *count > 1000 {
		log.Fatal("count must be 1–1000")
	}
	client, err := goruntrace.New(goruntrace.Config{Endpoint: *endpoint, Service: "example-order-service", Token: os.Getenv("GORUNTRACE_TOKEN"), MaxRetries: 2})
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Sending real observed runs of simulated business work; no external systems are contacted.")
	for i := 0; i < *count; i++ {
		switch i % 6 {
		case 0:
			_ = client.Trace(context.Background(), "sync-orders", func(context.Context) error {
				time.Sleep(80 * time.Millisecond)
				return errors.New("example: upstream inventory API returned HTTP 503")
			})
		case 1:
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
			_ = client.Trace(ctx, "sync-orders", func(ctx context.Context) error {
				<-ctx.Done()
				return fmt.Errorf("example: inventory request: %w", ctx.Err())
			})
			cancel()
		case 2:
			func() {
				defer func() { _ = recover() }()
				_ = client.Trace(context.Background(), "rebuild-index", func(context.Context) error {
					time.Sleep(30 * time.Millisecond)
					panic("example: index source unavailable")
				})
			}()
		default:
			name := []string{"send-email", "sync-stock", "generate-report"}[i%3]
			_ = client.Trace(context.Background(), name, func(context.Context) error { time.Sleep(time.Duration(20+i%5*15) * time.Millisecond); return nil })
		}
	}
	if err = client.Close(); err != nil {
		log.Println(err)
	}
	stats := client.Stats()
	log.Printf("SDK events: queued=%d sent=%d dropped=%d", stats.Queued, stats.Sent, stats.Dropped)
	if stats.Dropped > 0 {
		os.Exit(1)
	}
}
