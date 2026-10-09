package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const loadAveragePath = "/proc/loadavg"

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
