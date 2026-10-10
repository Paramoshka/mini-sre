package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIWatcherProcess(t *testing.T) {
	path := os.Getenv("MINI_SRE_TEST_CONFIG")
	if path == "" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("mini-sre", flag.ExitOnError)
	os.Args = []string{"mini-sre", "-config", path}
	if statePath := os.Getenv("MINI_SRE_TEST_STATE"); statePath != "" {
		os.Args = append(os.Args, "-telegram", "-telegram-state", statePath)
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestCLIStopsWhenConfigWatcherFails(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	if err := os.Mkdir(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "hosts.yaml")
	if err := os.WriteFile(path, []byte("hosts: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestCLIWatcherProcess$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "MINI_SRE_TEST_CONFIG="+path, "DEEPSEEK_API_KEY=test-key")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// The prompt means the watcher is armed and the CLI is waiting for input.
	if _, err := bufio.NewReader(stdout).ReadString('>'); err != nil {
		cmd.Wait()
		t.Fatalf("CLI did not start: %v, %s", err, stderr.String())
	}
	if err := os.RemoveAll(configDir); err != nil {
		cancel()
		cmd.Wait()
		t.Fatal(err)
	}
	err = cmd.Wait()
	if err == nil || ctx.Err() != nil || !strings.Contains(stderr.String(), "watched directory was removed or renamed") {
		t.Fatalf("fatal watcher error did not exit CLI: %v, %s", err, stderr.String())
	}
}

func TestTelegramProcessRejectsCorruptStateBeforePolling(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.yaml")
	statePath := filepath.Join(dir, "telegram.json")
	if err := os.WriteFile(path, []byte("telegram:\n  allowed_user_ids: [1]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	badState := `{"private-content":`
	if err := os.WriteFile(statePath, []byte(badState), 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestCLIWatcherProcess$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "MINI_SRE_TEST_CONFIG="+path, "MINI_SRE_TEST_STATE="+statePath,
		"DEEPSEEK_API_KEY=test-key", "TELEGRAM_BOT_TOKEN=test-token")
	output, err := cmd.CombinedOutput()
	if err == nil || ctx.Err() != nil || !strings.Contains(string(output), "telegram: state: invalid JSON") ||
		strings.Contains(string(output), "private-content") {
		t.Fatalf("Telegram did not fail safely at state restore: %v, %s", err, output)
	}
	after, err := os.ReadFile(statePath)
	if err != nil || string(after) != badState {
		t.Fatal("startup overwrote corrupt state")
	}
}
