package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"mini-sre/internal/remote"
)

const loadAveragePath = "/proc/loadavg"

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
			Name: "get_service_status", Description: "Read systemd service or Docker container state. backend defaults to systemd. Does not restart services.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"host_id":{"type":"string"},"service":{"type":"string","description":"Systemd service name or Docker container name/ID."},"backend":{"type":"string","enum":["systemd","docker"]}},"required":["service"],"additionalProperties":false}`),
		},
		{
			Name: "get_service_logs", Description: "Read recent journalctl or Docker container logs. backend defaults to systemd; omit service for the system journal. Defaults: 100 records from the last 60 minutes. No follow mode.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"host_id":{"type":"string"},"service":{"type":"string"},"backend":{"type":"string","enum":["systemd","docker"]},"lines":{"type":"integer","minimum":1,"maximum":500},"since_minutes":{"type":"integer","minimum":1}},"additionalProperties":false}`),
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
			case "get_service_status":
				out, err = t.ServiceStatus(ctx, args.HostID, args.Service, args.Backend)
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

func (t *Tools) LoadAverage(ctx context.Context, hostID string) (string, error) {
	result, err := t.command(ctx, hostID, "cat", loadAveragePath)
	if err != nil {
		return "", err
	}
	if result.Truncated {
		return "", errors.New("tools: load average output was truncated")
	}
	return formatLoadAverage(result.Stdout)
}

func formatLoadAverage(raw string) (string, error) {
	fields := strings.Fields(raw)
	if len(fields) < 3 {
		return "", fmt.Errorf("tools: unexpected /proc/loadavg format: %q", raw)
	}

	out := fmt.Sprintf("load average: %s %s %s", fields[0], fields[1], fields[2])
	if len(fields) >= 4 {
		if procs := strings.SplitN(fields[3], "/", 2); len(procs) == 2 {
			out += fmt.Sprintf(" (running %s/%s)", procs[0], procs[1])
		}
	}
	return out, nil
}

func (t *Tools) DiskUsage(ctx context.Context, hostID, path string) (string, error) {
	if path == "" {
		path = "/"
	}

	result, err := t.command(ctx, hostID, "df", "-B1", "--output=size,used,avail", "--", path)
	if err != nil {
		return "", err
	}
	if result.Truncated {
		return "", errors.New("tools: disk usage output was truncated")
	}
	rows := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(rows) != 2 {
		return "", errors.New("tools: unexpected df output")
	}
	fields := strings.Fields(rows[1])
	if len(fields) != 3 {
		return "", errors.New("tools: unexpected df columns")
	}
	var values [3]uint64
	for i, field := range fields {
		values[i], err = strconv.ParseUint(field, 10, 64)
		if err != nil {
			return "", errors.New("tools: invalid disk usage numbers")
		}
	}
	total, used, available := values[0], values[1], values[2]
	usedPercent := 0.0
	if total > 0 {
		usedPercent = float64(used) / float64(total) * 100
	}

	return fmt.Sprintf("%s: total=%s used=%s available=%s use=%.1f%%",
		path, humanBytes(total), humanBytes(used), humanBytes(available), usedPercent), nil
}

type toolArgs struct {
	HostID       string `json:"host_id"`
	Path         string `json:"path"`
	Service      string `json:"service"`
	Backend      string `json:"backend"`
	Lines        *int   `json:"lines"`
	SinceMinutes *int   `json:"since_minutes"`
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

var serviceName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:@\\-]*$`)

func serviceTarget(service, backend string, required bool) (string, string, error) {
	if backend == "" {
		backend = "systemd"
	}
	if backend != "systemd" && backend != "docker" {
		return "", "", errors.New("tools: backend must be systemd or docker")
	}
	if service == "" {
		if required || backend == "docker" {
			return "", "", errors.New("tools: service is required")
		}
		return service, backend, nil
	}
	if !serviceName.MatchString(service) {
		return "", "", errors.New("tools: invalid service name; use one service or container ID, without patterns")
	}
	if backend == "systemd" && !strings.HasSuffix(service, ".service") {
		service += ".service"
	}
	return service, backend, nil
}

func (t *Tools) ServiceStatus(ctx context.Context, hostID, service, backend string) (string, error) {
	service, backend, err := serviceTarget(service, backend, true)
	if err != nil {
		return "", err
	}
	var result remote.Result
	if backend == "systemd" {
		result, err = t.command(ctx, hostID, "systemctl", "show", "--no-pager", "--property=LoadState,ActiveState,SubState,Result,ExecMainStatus", "--", service)
		if err == nil && strings.Contains(result.Stdout, "LoadState=not-found") {
			return "", fmt.Errorf("tools: service %q not found", service)
		}
	} else {
		result, err = t.command(ctx, hostID, "docker", "inspect", "--type=container", "--format={{json .State}}", "--", service)
	}
	if err != nil {
		return "", err
	}
	return commandOutput(result), nil
}

func (t *Tools) ServiceLogs(ctx context.Context, args toolArgs) (string, error) {
	service, backend, err := serviceTarget(args.Service, args.Backend, false)
	if err != nil {
		return "", err
	}
	lines, minutes := 100, 60
	if args.Lines != nil {
		lines = *args.Lines
	}
	if args.SinceMinutes != nil {
		minutes = *args.SinceMinutes
	}
	if lines < 1 || lines > 500 || minutes < 1 {
		return "", errors.New("tools: lines must be 1..500 and since_minutes must be positive")
	}
	var result remote.Result
	if backend == "systemd" {
		flags := []string{"--no-pager", "--output=short-iso", "--lines=" + strconv.Itoa(lines), "--since=-" + strconv.Itoa(minutes) + "min"}
		if service != "" {
			flags = append(flags, "--unit="+service)
		}
		result, err = t.command(ctx, args.HostID, "journalctl", flags...)
	} else {
		result, err = t.command(ctx, args.HostID, "docker", "logs", "--timestamps", "--tail="+strconv.Itoa(lines), "--since="+strconv.Itoa(minutes)+"m", "--", service)
	}
	if err != nil {
		return "", err
	}
	out := commandOutput(result)
	if strings.TrimSpace(out) == "" {
		return "No log entries in the requested interval.", nil
	}
	return out, nil
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

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}

	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
