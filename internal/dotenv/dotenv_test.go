package dotenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	input := `
# comment
DEEPSEEK_API_KEY=sk-test

export PLAIN=value
QUOTED="hello world"
SINGLE='single'
EMPTY=
  SPACED  =  padded
`
	want := map[string]string{
		"DEEPSEEK_API_KEY": "sk-test",
		"PLAIN":            "value",
		"QUOTED":           "hello world",
		"SINGLE":           "single",
		"EMPTY":            "",
		"SPACED":           "padded",
	}

	values, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(values) != len(want) {
		t.Fatalf("Parse = %v, want %v", values, want)
	}
	for key, value := range want {
		if values[key] != value {
			t.Errorf("values[%q] = %q, want %q", key, values[key], value)
		}
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"missing equals", "NOEQUALS", "line 1: missing '='"},
		{"empty key", "=value", "line 1: empty key"},
		{"error on second line", "KEY=value\nBROKEN", "line 2: missing '='"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.input))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Parse error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("MINI_SRE_TEST_FILE=from-file\nMINI_SRE_TEST_BOTH=from-file\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	os.Unsetenv("MINI_SRE_TEST_FILE")
	t.Cleanup(func() { os.Unsetenv("MINI_SRE_TEST_FILE") })
	t.Setenv("MINI_SRE_TEST_BOTH", "from-env")

	if err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := os.Getenv("MINI_SRE_TEST_FILE"); got != "from-file" {
		t.Errorf("MINI_SRE_TEST_FILE = %q, want %q", got, "from-file")
	}
	if got := os.Getenv("MINI_SRE_TEST_BOTH"); got != "from-env" {
		t.Errorf("MINI_SRE_TEST_BOTH = %q, want env to win over file", got)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if err := Load(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Errorf("Load(missing) = %v, want nil", err)
	}
}

func TestLoadParseError(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("BROKEN\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := Load(path); err == nil {
		t.Error("Load(broken) = nil, want error")
	}
}
