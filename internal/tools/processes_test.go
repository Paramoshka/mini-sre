package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"mini-sre/internal/config"
	"mini-sre/internal/remote"
)

const processHeader = "    PID USER     %CPU %MEM   RSS COMMAND\n"

func TestTopProcessesSortAndLimit(t *testing.T) {
	for _, tc := range []struct {
		name, raw, sort string
		wantRows        int
	}{
		{"defaults", `{}`, "-rss", 10},
		{"memory", `{"sort_by":"memory","limit":2}`, "-rss", 2},
		{"cpu", `{"sort_by":"cpu","limit":1}`, "-pcpu", 1},
		{"maximum", `{"limit":50}`, "-rss", 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := processHeader + strings.Repeat("     42 sre      150.0  2.5  102400 worker name\n", 12)
			argsFile := fakeCommand(t, "ps", output, "", "0")
			call := Registry(&remote.Runner{})["get_top_processes"]
			if call == nil {
				t.Fatal("process tool is not registered")
			}
			out, err := call(context.Background(), json.RawMessage(tc.raw))
			if err != nil || strings.Count(out, "worker name") != tc.wantRows ||
				!strings.Contains(out, "lifetime average") || !strings.Contains(out, "RSS: KiB") ||
				!strings.Contains(out, "150.0") {
				t.Fatalf("wrong process report: %q, %v", out, err)
			}
			args, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			want := "-eo\npid,user,pcpu,pmem,rss,comm\n--sort=" + tc.sort + "\n--cols=256\n"
			if string(args) != want {
				t.Fatalf("unsafe or incorrect ps arguments: %q, want %q", args, want)
			}
		})
	}
}

func TestTopProcessesRejectsArgumentsBeforeExecution(t *testing.T) {
	argsFile := fakeCommand(t, "ps", processHeader, "", "0")
	call := Registry(&remote.Runner{})["get_top_processes"]
	if call == nil {
		t.Fatal("process tool is not registered")
	}
	for _, raw := range []string{
		`{"sort_by":"rss"}`, `{"sort_by":"cpu; touch injected"}`,
		`{"limit":0}`, `{"limit":-1}`, `{"limit":51}`, `{"limit":1.5}`,
		`{"limit":"10"}`, `{"command":"kill"}`, `[]`, `null`, `{bad`,
	} {
		if _, err := call(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid arguments accepted: %s", raw)
		}
	}
	if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
		t.Fatal("ps ran before argument validation")
	}
}

func TestTopProcessesErrorsAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		name, stdout, stderr, exit string
		wantErr                    bool
	}{
		{"command error", "", "ps permission denied: secret", "1", true},
		{"empty output", "", "", "0", true},
		{"no rows", processHeader, "", "0", true},
		{"truncated after top rows", processHeader + strings.Repeat("     42 sre      1.0  0.1  1024 secret-worker\n", 2000), "", "0", false},
		{"truncated first row", processHeader + strings.Repeat("x", 70000), "", "0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeCommand(t, "ps", tc.stdout, tc.stderr, tc.exit)
			runner := &remote.Runner{Config: config.Config{Hosts: map[string]config.Host{"web": {Password: "secret"}}}}
			call := Registry(runner)["get_top_processes"]
			if call == nil {
				t.Fatal("process tool is not registered")
			}
			out, err := call(context.Background(), nil)
			if (err != nil) != tc.wantErr || strings.Contains(out, "secret") || (err != nil && strings.Contains(err.Error(), "secret")) {
				t.Fatalf("report or error leaked password: %q, %v", out, err)
			}
			if !tc.wantErr && strings.Count(out, "[redacted]-worker") != 10 {
				t.Fatal("truncated ps output lost the requested complete top rows")
			}
		})
	}
}

func TestTopProcessesLocally(t *testing.T) {
	call := Registry(&remote.Runner{})["get_top_processes"]
	if call == nil {
		t.Fatal("process tool is not registered")
	}
	for _, sortBy := range []string{"memory", "cpu"} {
		out, err := call(context.Background(), json.RawMessage(`{"sort_by":"`+sortBy+`","limit":3}`))
		if err != nil || !strings.Contains(out, "COMMAND") || !strings.Contains(out, "PID") {
			t.Fatalf("real local ps report: %q, %v", out, err)
		}
	}
	if _, err := call(context.Background(), json.RawMessage(`{"host_id":"missing"}`)); err == nil {
		t.Fatal("unknown SSH host accepted")
	}
}
