package mcpserver

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Singmin/GorunTrace/internal/api"
	"github.com/Singmin/GorunTrace/internal/storage"
	goruntrace "github.com/Singmin/GorunTrace/sdk"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestReadOnlyToolsAgainstRealRuns(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	httpServer := httptest.NewServer(api.New(store, "secret", nil))
	defer httpServer.Close()
	sdk, err := goruntrace.New(goruntrace.Config{Endpoint: httpServer.URL, Service: "mcp-test", Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	_ = sdk.Trace(context.Background(), "sync", func(context.Context) error { return errors.New("upstream unavailable") })
	if err = sdk.Close(); err != nil {
		t.Fatal(err)
	}
	server, err := New(httpServer.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "integration-test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 5 {
		t.Fatal("expected five read-only tools", len(list.Tools))
	}
	for _, tool := range list.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatal("missing read-only annotation", tool.Name)
		}
		if tool.Name == "get_run_details" {
			continue
		}
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: map[string]any{"service": "mcp-test"}})
		if err != nil || result.IsError {
			t.Fatal(tool.Name, result, err)
		}
	}
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_failed_runs", Arguments: map[string]any{"limit": 1}})
	if err != nil || result.IsError {
		t.Fatal(result, err)
	}
	data := result.StructuredContent.(map[string]any)
	if data["total"] != float64(1) {
		t.Fatal(data)
	}
	items := data["items"].([]any)
	id := items[0].(map[string]any)["run_id"].(string)
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_run_details", Arguments: map[string]any{"run_id": id}})
	if err != nil || result.IsError {
		t.Fatal(result, err)
	}
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_task_runs", Arguments: map[string]any{"limit": 999}})
	if err != nil || !result.IsError {
		t.Fatal("invalid input should be a tool error", result, err)
	}
}
