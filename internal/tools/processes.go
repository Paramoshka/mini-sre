package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (t *Tools) TopProcesses(ctx context.Context, args toolArgs) (string, error) {
	sortBy := args.SortBy
	if sortBy == "" {
		sortBy = "memory"
	}
	sortKey := ""
	switch sortBy {
	case "memory":
		sortKey = "-rss"
	case "cpu":
		sortKey = "-pcpu"
	default:
		return "", errors.New("tools: sort_by must be memory or cpu")
	}
	limit := 10
	if args.Limit != nil {
		limit = *args.Limit
	}
	if limit < 1 || limit > 50 {
		return "", errors.New("tools: limit must be 1..50")
	}
	result, err := t.command(ctx, args.HostID, "ps", "-eo", "pid,user,pcpu,pmem,rss,comm", "--sort="+sortKey, "--cols=256")
	if err != nil {
		return "", err
	}
	rows := strings.Split(strings.TrimRight(result.Stdout, "\n"), "\n")
	if result.Truncated && !strings.HasSuffix(result.Stdout, "\n") {
		rows = rows[:len(rows)-1]
	}
	if len(rows) < 2 {
		return "", errors.New("tools: ps returned no complete process rows")
	}
	if result.Truncated && len(rows)-1 < limit {
		return "", errors.New("tools: ps output was truncated before the requested top processes")
	}
	count := min(limit, len(rows)-1)
	return fmt.Sprintf("Top %d processes by %s (CPU: lifetime average; RSS: KiB):\n%s",
		count, sortBy, strings.Join(rows[:count+1], "\n")), nil
}
