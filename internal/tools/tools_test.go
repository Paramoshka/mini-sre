package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestFormatLoadAverage(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "full",
			raw:  "0.45 0.32 0.28 1/1234 5678\n",
			want: "load average: 0.45 0.32 0.28 (running 1/1234, last pid 5678)",
		},
		{
			name: "no pid",
			raw:  "1.00 0.50 0.25 3/200",
			want: "load average: 1.00 0.50 0.25 (running 3/200)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := formatLoadAverage(tt.raw)
			if err != nil {
				t.Fatalf("formatLoadAverage: %v", err)
			}
			if got != tt.want {
				t.Errorf("formatLoadAverage = %q, want %q", got, tt.want)
			}
		})
	}

	for _, raw := range []string{"", "0.1 0.2", "a b c 1/2 3", "0.1 0.2 0.3 nope", "0.1 0.2 0.3 1/2 x"} {
		t.Run("invalid_"+raw, func(t *testing.T) {
			if _, err := formatLoadAverage(raw); err == nil {
				t.Errorf("formatLoadAverage(%q) = nil error, want error", raw)
			}
		})
	}
}

func TestLoadAverage(t *testing.T) {
	if _, err := os.Stat(loadAveragePath); err != nil {
		t.Skipf("%s unavailable: %v", loadAveragePath, err)
	}

	got, err := LoadAverage(context.Background())
	if err != nil {
		t.Fatalf("LoadAverage: %v", err)
	}
	if !strings.HasPrefix(got, "load average: ") {
		t.Errorf("LoadAverage = %q, want load average prefix", got)
	}
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		in   uint64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{5 * 1024 * 1024 * 1024, "5.0 GiB"},
	}
	for _, tt := range tests {
		if got := humanBytes(tt.in); got != tt.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDiskUsage(t *testing.T) {
	dir := t.TempDir()
	got, err := DiskUsage(context.Background(), dir)
	if err != nil {
		t.Fatalf("DiskUsage(%q): %v", dir, err)
	}
	if !strings.HasPrefix(got, dir+": total=") || !strings.Contains(got, "use=") {
		t.Errorf("DiskUsage = %q, want %q prefix with usage", got, dir)
	}

	root, err := DiskUsage(context.Background(), "")
	if err != nil {
		t.Fatalf("DiskUsage(default): %v", err)
	}
	if !strings.HasPrefix(root, "/: total=") {
		t.Errorf("DiskUsage(default) = %q, want / prefix", root)
	}

	if _, err := DiskUsage(context.Background(), dir+"/missing"); err == nil {
		t.Error("DiskUsage(missing) = nil error, want error")
	}
}

func TestRegistry(t *testing.T) {
	registry := Registry()

	load, ok := registry["get_load_average"]
	if !ok {
		t.Fatal("registry missing get_load_average")
	}
	if _, err := load(context.Background(), nil); err != nil {
		t.Errorf("get_load_average: %v", err)
	}

	disk, ok := registry["get_disk_usage"]
	if !ok {
		t.Fatal("registry missing get_disk_usage")
	}

	out, err := disk(context.Background(), json.RawMessage(`{"path":"/"}`))
	if err != nil {
		t.Fatalf("get_disk_usage: %v", err)
	}
	if !strings.HasPrefix(out, "/: total=") {
		t.Errorf("get_disk_usage = %q, want / prefix", out)
	}

	out, err = disk(context.Background(), nil)
	if err != nil {
		t.Fatalf("get_disk_usage(default): %v", err)
	}
	if !strings.HasPrefix(out, "/: total=") {
		t.Errorf("get_disk_usage(default) = %q, want / prefix", out)
	}

	if _, err := disk(context.Background(), json.RawMessage(`{bad`)); err == nil {
		t.Error("get_disk_usage(invalid args) = nil error, want error")
	}
}

func TestSpecs(t *testing.T) {
	specs := Specs()
	if len(specs) != 2 {
		t.Fatalf("Specs() = %d, want 2", len(specs))
	}
	registry := Registry()
	for _, spec := range specs {
		if _, ok := registry[spec.Name]; !ok {
			t.Errorf("spec %q has no registry entry", spec.Name)
		}
		if spec.Description == "" {
			t.Errorf("spec %q has empty description", spec.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(spec.Parameters, &schema); err != nil {
			t.Errorf("spec %q parameters: %v", spec.Name, err)
		}
	}
}
