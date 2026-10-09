package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"mini-sre/internal/remote"
)

var serviceName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:@\\-]*$`)

type ServiceTarget struct {
	Name    string `json:"service"`
	Backend string `json:"backend"`
	Scope   string `json:"scope"`
}

func (s ServiceTarget) normalized(required bool) (ServiceTarget, error) {
	if s.Backend == "" {
		s.Backend = "systemd"
	}
	if s.Backend != "systemd" && s.Backend != "docker" {
		return ServiceTarget{}, errors.New("tools: backend must be systemd or docker")
	}
	if s.Backend == "systemd" {
		if s.Scope == "" {
			s.Scope = "system"
		}
		if s.Scope != "system" && s.Scope != "user" {
			return ServiceTarget{}, errors.New("tools: scope must be system or user")
		}
	} else if s.Scope != "" {
		return ServiceTarget{}, errors.New("tools: scope is only supported for systemd")
	}
	if s.Name == "" {
		if required || s.Backend == "docker" {
			return ServiceTarget{}, errors.New("tools: service is required")
		}
		return s, nil
	}
	if !serviceName.MatchString(s.Name) {
		return ServiceTarget{}, errors.New("tools: invalid service name; use one service or container ID, without patterns")
	}
	if s.Backend == "systemd" && !strings.HasSuffix(s.Name, ".service") {
		s.Name += ".service"
	}
	return s, nil
}

func (t *Tools) ServiceStatus(ctx context.Context, hostID string, target ServiceTarget) (string, error) {
	target, err := target.normalized(true)
	if err != nil {
		return "", err
	}
	var result remote.Result
	if target.Backend == "systemd" {
		flags := []string{"show", "--no-pager", "--property=LoadState,ActiveState,SubState,Result,ExecMainStatus"}
		if target.Scope == "user" {
			flags = append(flags, "--user")
		}
		flags = append(flags, "--", target.Name)
		result, err = t.command(ctx, hostID, "systemctl", flags...)
		if err == nil && strings.Contains(result.Stdout, "LoadState=not-found") {
			return "", fmt.Errorf("tools: service %q not found in %s scope", target.Name, target.Scope)
		}
	} else {
		result, err = t.command(ctx, hostID, "docker", "inspect", "--type=container", "--format={{json .State}}", "--", target.Name)
	}
	if err != nil {
		return "", err
	}
	return commandOutput(result), nil
}

func (t *Tools) ServiceLogs(ctx context.Context, args toolArgs) (string, error) {
	target, err := args.ServiceTarget.normalized(false)
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
	if target.Backend == "systemd" {
		flags := []string{"--no-pager", "--output=short-iso", "--lines=" + strconv.Itoa(lines), "--since=-" + strconv.Itoa(minutes) + "min"}
		if target.Scope == "user" {
			flags = append(flags, "--user")
		}
		if target.Name != "" {
			if target.Scope == "user" {
				flags = append(flags, "--user-unit="+target.Name)
			} else {
				flags = append(flags, "--unit="+target.Name)
			}
		}
		result, err = t.command(ctx, args.HostID, "journalctl", flags...)
	} else {
		result, err = t.command(ctx, args.HostID, "docker", "logs", "--timestamps", "--tail="+strconv.Itoa(lines), "--since="+strconv.Itoa(minutes)+"m", "--", target.Name)
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
