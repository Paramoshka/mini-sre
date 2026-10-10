package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"mini-sre/internal/remote"
)

type Spec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type Func func(ctx context.Context, args json.RawMessage) (string, error)

type Tools struct {
	Runner *remote.Runner
}

func Specs() []Spec {
	return []Spec{
		{
			Name: "list_hosts", Description: "List available host IDs. local is the machine running the tool executor.",
			Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		},
		{
			Name:        "get_load_average",
			Description: "Get host load average for the last 1, 5 and 15 minutes with running/total process counts. host_id defaults to local.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"host_id":{"type":"string","description":"Host ID from list_hosts; default local."}},"additionalProperties":false}`),
		},
		{
			Name:        "get_disk_usage",
			Description: "Get disk usage (total, used, available, percent used) for a filesystem path.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"host_id":{"type":"string","description":"Host ID from list_hosts; default local."},"path":{"type":"string","description":"Filesystem path to inspect; default /."}},"additionalProperties":false}`),
		},
		{
			Name:        "get_top_processes",
			Description: "List visible processes sorted by memory (RSS) or CPU, descending. Shows PID, user, CPU%, MEM%, RSS in KiB and executable name, without command-line arguments. CPU% is a lifetime average, not an interval measurement, and can exceed 100% on multiple cores. Defaults: host_id=local, sort_by=memory, limit=10.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"host_id":{"type":"string","description":"Host ID from list_hosts; default local."},"sort_by":{"type":"string","enum":["memory","cpu"],"description":"Sort descending by resident memory or lifetime-average CPU; default memory."},"limit":{"type":"integer","minimum":1,"maximum":50,"description":"Maximum number of processes; default 10."}},"additionalProperties":false}`),
		},
		{
			Name: "get_service_status", Description: "Read systemd service or Docker container state. backend defaults to systemd; scope defaults to system. Use scope=user for user systemd services. Does not restart services.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"host_id":{"type":"string"},"service":{"type":"string","description":"Systemd service name or Docker container name/ID."},"backend":{"type":"string","enum":["systemd","docker"]},"scope":{"type":"string","enum":["system","user"],"description":"Systemd only: system (default) or the executing user's service manager."}},"required":["service"],"additionalProperties":false}`),
		},
		{
			Name: "get_service_logs", Description: "Read recent journalctl or Docker container logs. backend defaults to systemd; scope defaults to system. Use scope=user for user systemd services; omit service for the selected journal. Defaults: 100 records from the last 60 minutes. No follow mode.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"host_id":{"type":"string"},"service":{"type":"string"},"backend":{"type":"string","enum":["systemd","docker"]},"scope":{"type":"string","enum":["system","user"],"description":"Systemd only: system (default) or the executing user's journal."},"lines":{"type":"integer","minimum":1,"maximum":500},"since_minutes":{"type":"integer","minimum":1}},"additionalProperties":false}`),
		},
	}
}

func Registry(runner *remote.Runner) map[string]Func {
	t := &Tools{Runner: runner}
	registry := map[string]Func{}
	for _, spec := range Specs() {
		name := spec.Name
		registry[name] = func(ctx context.Context, raw json.RawMessage) (string, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return "", err
			}
			var out string
			switch name {
			case "list_hosts":
				out = strings.Join(runner.Hosts(), "\n")
			case "get_load_average":
				out, err = t.LoadAverage(ctx, args.HostID)
			case "get_disk_usage":
				out, err = t.DiskUsage(ctx, args.HostID, args.Path)
			case "get_top_processes":
				out, err = t.TopProcesses(ctx, args)
			case "get_service_status":
				out, err = t.ServiceStatus(ctx, args.HostID, args.ServiceTarget)
			case "get_service_logs":
				out, err = t.ServiceLogs(ctx, args)
			default:
				return "", errors.New("tools: no handler for tool")
			}
			if err != nil {
				return "", errors.New(runner.Redact(err.Error()))
			}
			return runner.Redact(out), nil
		}
	}
	return registry
}

type toolArgs struct {
	HostID string `json:"host_id"`
	Path   string `json:"path"`
	ServiceTarget
	Lines        *int   `json:"lines"`
	SinceMinutes *int   `json:"since_minutes"`
	SortBy       string `json:"sort_by"`
	Limit        *int   `json:"limit"`
}

func decodeArgs(raw json.RawMessage) (toolArgs, error) {
	var args toolArgs
	if len(raw) == 0 {
		return args, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		return args, errors.New("tools: arguments must be a JSON object")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return args, errors.New("tools: invalid arguments; check field names and types")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return args, errors.New("tools: expected one argument object")
	}
	return args, nil
}

func (t *Tools) command(ctx context.Context, hostID, program string, args ...string) (remote.Result, error) {
	for _, arg := range args {
		if strings.ContainsRune(arg, '\x00') {
			return remote.Result{}, errors.New("tools: arguments must not contain NUL bytes")
		}
	}
	result, err := t.Runner.Run(ctx, hostID, program, args...)
	if err != nil {
		return result, fmt.Errorf("tools: %w; %s", err, commandOutput(result))
	}
	return result, nil
}

func commandOutput(result remote.Result) string {
	out := result.Stdout
	if result.Stderr != "" {
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += result.Stderr
	}
	if result.Truncated {
		out += "\n[output truncated at 64 KiB]"
	}
	return out
}
