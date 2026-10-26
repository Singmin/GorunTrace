package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Singmin/GorunTrace/internal/storage"
	goruntrace "github.com/Singmin/GorunTrace/sdk"
)

func TestSDKToAPIIntegration(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := httptest.NewServer(New(s, "test-key", nil))
	defer server.Close()
	c, err := goruntrace.New(goruntrace.Config{Endpoint: server.URL, Service: "integration", Token: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Trace(context.Background(), "sync", func(context.Context) error { return errors.New("token=supersecret upstream failed") })
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, token string
		code        int
	}{{"/api/v1/runs", "", 401}, {"/api/v1/runs?limit=-1", "test-key", 400}, {"/api/v1/runs?from=nope", "test-key", 400}, {"/api/v1/runs/missing", "test-key", 404}, {"/api/v1/runs", "test-key", 200}, {"/healthz", "", 200}} {
		req, _ := http.NewRequest("GET", server.URL+tc.path, nil)
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != tc.code {
			t.Error(tc.path, resp.StatusCode)
		}
		if tc.path == "/api/v1/runs" && tc.code == 200 {
			var p storage.Page[storage.Run]
			_ = json.NewDecoder(resp.Body).Decode(&p)
			if p.Total != 1 || p.Items[0].ErrorSummary != "token=[REDACTED] upstream failed" {
				t.Fatal(p)
			}
		}
		resp.Body.Close()
	}
	for _, body := range []string{`{"events":[]}`, `{"events":[],"unknown":true}`, `{"events":[]} {}`, `no json`} {
		req, _ := http.NewRequest("POST", server.URL+"/api/v1/events/batch", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer test-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatal(body, resp.StatusCode)
		}
	}
}
