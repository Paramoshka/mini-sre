package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"mini-sre/internal/agent"
	"mini-sre/internal/config"
	"mini-sre/internal/remote"
	"mini-sre/internal/session"
)

func TestConfiguredAgentUsesProbesWithoutExposingCredentials(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hosts.yaml")
	content := "hosts:\n  web-1:\n    address: 192.0.2.123\n    user: sre\n    password: test-ssh-secret\n  storage-1:\n    address: 192.0.2.124\n    user: sre\n    key_file: private-key\n"
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	scripts := map[string]string{
		"systemctl":  "#!/bin/sh\nprintf 'LoadState=loaded\\nActiveState=active\\nSubState=running\\n'\n",
		"journalctl": "#!/bin/sh\nprintf 'app warning: test-ssh-secret\\n'\n",
		"docker":     "#!/bin/sh\ncase \"$1\" in\ninspect) printf '{\"Status\":\"running\"}\\n';;\nlogs) printf 'container warning: test-ssh-secret\\n' >&2;;\nesac\n",
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		for _, secret := range []string{"test-ssh-secret", "192.0.2.123", "192.0.2.124", "private-key"} {
			if strings.Contains(string(body), secret) {
				t.Errorf("model request exposed config value %q", secret)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if requests.Add(1) == 1 {
			var req struct {
				Tools []struct {
					Function struct{ Name string } `json:"function"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(body, &req); err != nil || len(req.Tools) != 5 {
				t.Errorf("tool definitions missing: %v, %d", err, len(req.Tools))
			}
			io.WriteString(w, `{"id":"first","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"hosts","type":"function","function":{"name":"list_hosts","arguments":"{}"}},{"id":"la","type":"function","function":{"name":"get_load_average","arguments":"{}"}},{"id":"disk","type":"function","function":{"name":"get_disk_usage","arguments":"{\"path\":\"/\"}"}},{"id":"systemd","type":"function","function":{"name":"get_service_status","arguments":"{\"service\":\"nginx\"}"}},{"id":"docker","type":"function","function":{"name":"get_service_status","arguments":"{\"service\":\"web\",\"backend\":\"docker\"}"}},{"id":"journal","type":"function","function":{"name":"get_service_logs","arguments":"{}"}},{"id":"container_logs","type":"function","function":{"name":"get_service_logs","arguments":"{\"service\":\"web\",\"backend\":\"docker\"}"}}]}}]}`)
			return
		}
		var req struct {
			Messages []struct {
				Role       string `json:"role"`
				Content    string `json:"content"`
				ToolCallID string `json:"tool_call_id"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Error(err)
		}
		results := map[string]string{}
		for _, m := range req.Messages {
			if m.Role == "tool" {
				results[m.ToolCallID] = m.Content
				if strings.HasPrefix(m.Content, "error:") {
					t.Errorf("probe failed: %s", m.Content)
				}
			}
		}
		if len(results) != 7 || !strings.Contains(results["hosts"], "web-1") || !strings.Contains(results["disk"], "available=") || !strings.Contains(results["la"], "load average:") || !strings.Contains(results["journal"], "[redacted]") || !strings.Contains(results["container_logs"], "[redacted]") {
			t.Errorf("probe results missing: %+v", results)
		}
		io.WriteString(w, `{"id":"final","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Проверено"}}]}`)
	}))
	defer server.Close()
	client, err := agent.New(agent.Config{APIKey: "test-key", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	s := &session.Session{Turn: client.Chat, Tools: toolSpecs(), RunTool: toolRunner(&remote.Runner{Config: cfg})}
	resp, err := s.Ask(context.Background(), "Проверь local")
	if err != nil || resp.Content != "Проверено" || requests.Load() != 2 {
		t.Fatalf("agent/tools flow: response=%+v err=%v requests=%d", resp, err, requests.Load())
	}
}
