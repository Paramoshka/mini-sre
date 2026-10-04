package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const loadAveragePath = "/proc/loadavg"

type Spec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type Func func(ctx context.Context, args json.RawMessage) (string, error)

func Specs() []Spec {
	return []Spec{
		{
			Name:        "get_load_average",
			Description: "Get host load average for the last 1, 5 and 15 minutes with running/total process counts.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
		{
			Name:        "get_disk_usage",
			Description: "Get disk usage (total, used, available, percent used) for a filesystem path.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Filesystem path to inspect, for example / or /var."}}}`),
		},
	}
}

func Registry() map[string]Func {
	return map[string]Func{
		"get_load_average": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return LoadAverage(ctx)
		},
		"get_disk_usage": getDiskUsage,
	}
}

func LoadAverage(_ context.Context) (string, error) {
	raw, err := os.ReadFile(loadAveragePath)
	if err != nil {
		return "", fmt.Errorf("tools: read %s: %w", loadAveragePath, err)
	}
	return formatLoadAverage(string(raw))
}

func formatLoadAverage(raw string) (string, error) {
	fields := strings.Fields(raw)
	if len(fields) < 4 {
		return "", fmt.Errorf("tools: unexpected /proc/loadavg format: %q", raw)
	}

	for _, value := range fields[:3] {
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return "", fmt.Errorf("tools: parse load average %q: %w", value, err)
		}
	}

	procs := strings.SplitN(fields[3], "/", 2)
	if len(procs) != 2 {
		return "", fmt.Errorf("tools: unexpected running/total %q", fields[3])
	}
	for _, value := range procs {
		if _, err := strconv.Atoi(value); err != nil {
			return "", fmt.Errorf("tools: parse running/total %q: %w", fields[3], err)
		}
	}

	lastPID := ""
	if len(fields) >= 5 {
		if _, err := strconv.Atoi(fields[4]); err != nil {
			return "", fmt.Errorf("tools: parse last pid %q: %w", fields[4], err)
		}
		lastPID = ", last pid " + fields[4]
	}

	return fmt.Sprintf("load average: %s %s %s (running %s/%s%s)",
		fields[0], fields[1], fields[2], procs[0], procs[1], lastPID), nil
}

func DiskUsage(_ context.Context, path string) (string, error) {
	if path == "" {
		path = "/"
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return "", fmt.Errorf("tools: statfs %q: %w", path, err)
	}
	if stat.Bsize <= 0 {
		return "", fmt.Errorf("tools: statfs %q: invalid block size %d", path, stat.Bsize)
	}

	blockSize := uint64(stat.Bsize)
	total := stat.Blocks * blockSize
	used := total - stat.Bfree*blockSize
	available := stat.Bavail * blockSize

	usedPercent := 0.0
	if total > 0 {
		usedPercent = float64(used) / float64(total) * 100
	}

	return fmt.Sprintf("%s: total=%s used=%s available=%s use=%.1f%%",
		path, humanBytes(total), humanBytes(used), humanBytes(available), usedPercent), nil
}

func getDiskUsage(ctx context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Path string `json:"path"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &input); err != nil {
			return "", fmt.Errorf("tools: get_disk_usage: invalid arguments: %w", err)
		}
	}
	return DiskUsage(ctx, input.Path)
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
