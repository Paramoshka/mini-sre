package telegram

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func entityText(t *testing.T, part messagePart, entity messageEntity) string {
	t.Helper()
	units := utf16.Encode([]rune(part.Text))
	if entity.Offset < 0 || entity.Length <= 0 || entity.Offset+entity.Length > len(units) {
		t.Fatalf("entity out of bounds: %+v", entity)
	}
	return string(utf16.Decode(units[entity.Offset : entity.Offset+entity.Length]))
}

func TestFormatReplyCommands(t *testing.T) {
	input := "🙂 **Permissions**\n\n```bash\nsudo tee /home/mini-sre/.ssh/authorized_keys < ~/mini-sre.pub\nprintf '%s' '$HOME & <tag> **literal** `whoami`'\n```\n\nCheck `journalctl --user -u mini-sre` and /proc/loadavg."
	parts := formatReply(input)
	if len(parts) != 1 {
		t.Fatalf("parts=%d, want 1", len(parts))
	}
	part := parts[0]
	want := "🙂 Permissions\n\nsudo tee /home/mini-sre/.ssh/authorized_keys < ~/mini-sre.pub\nprintf '%s' '$HOME & <tag> **literal** `whoami`'\n\nCheck journalctl --user -u mini-sre and /proc/loadavg."
	if part.Text != want {
		t.Fatalf("formatted text=%q, want %q", part.Text, want)
	}
	if len(part.Entities) != 3 {
		t.Fatalf("entities=%+v, want bold, pre, code", part.Entities)
	}
	if part.Entities[0].Type != "bold" || part.Entities[0].Offset != 3 || entityText(t, part, part.Entities[0]) != "Permissions" {
		t.Fatal("bold entity or UTF-16 offset incorrect")
	}
	if part.Entities[1].Type != "pre" || part.Entities[1].Language != "bash" || entityText(t, part, part.Entities[1]) != "sudo tee /home/mini-sre/.ssh/authorized_keys < ~/mini-sre.pub\nprintf '%s' '$HOME & <tag> **literal** `whoami`'\n" {
		t.Fatal("shell code was changed or not marked as preformatted")
	}
	if part.Entities[2].Type != "code" || entityText(t, part, part.Entities[2]) != "journalctl --user -u mini-sre" {
		t.Fatal("inline command not formatted")
	}
}

func TestFormatReplyEdgeCases(t *testing.T) {
	tests := []struct {
		name, input, want string
		entities          []messageEntity
	}{
		{"plain", "<b>text</b> & a_b*c", "<b>text</b> & a_b*c", nil},
		{"unclosed fence", "```bash\necho '**literal** `literal`'", "```bash\necho '**literal** `literal`'", nil},
		{"unclosed inline", "use `df and **unclosed", "use `df and **unclosed", nil},
		{"indented fence", "  ```\n  cmd\n  ```\n", "  cmd\n", []messageEntity{{Type: "pre", Length: 6}}},
		{"CRLF", "```sh\r\ncmd\r\n```\r\n", "cmd\r\n", []messageEntity{{Type: "pre", Length: 5, Language: "sh"}}},
		{"empty fence", "```\n```", "```\n```", nil},
		{"blank fence", "```\n \n```", "```\n \n```", nil},
		{"backtick in code", "``echo `whoami` ``", "echo `whoami` ", []messageEntity{{Type: "code", Length: 14}}},
		{"code inside bold", "**Run `df` now**", "Run df now", []messageEntity{{Type: "bold", Length: 4}, {Type: "code", Offset: 4, Length: 2}, {Type: "bold", Offset: 6, Length: 4}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts := formatReply(tt.input)
			if len(parts) != 1 || parts[0].Text != tt.want || !reflect.DeepEqual(parts[0].Entities, tt.entities) {
				t.Fatalf("parts=%+v, want text=%q entities=%+v", parts, tt.want, tt.entities)
			}
		})
	}
}

func TestFormatReplySplitsLongCode(t *testing.T) {
	code := strings.Repeat("echo 'Hello🙂 & <tag>'\n", 500)
	parts := formatReply("**Commands**\n```bash\n" + code + "```\nDone.")
	var all, formattedCode strings.Builder
	for _, part := range parts {
		if part.Text == "" || !utf8.ValidString(part.Text) || len(utf16.Encode([]rune(part.Text))) > 4000 {
			t.Fatal("invalid chunk or length")
		}
		all.WriteString(part.Text)
		for _, entity := range part.Entities {
			text := entityText(t, part, entity)
			if entity.Type == "pre" {
				if entity.Language != "bash" || !strings.HasSuffix(text, "\n") {
					t.Fatal("language lost or a command was split across messages")
				}
				formattedCode.WriteString(text)
			}
		}
	}
	if len(parts) < 2 || all.String() != "Commands\n"+code+"Done." || formattedCode.String() != code {
		t.Fatal("splitting lost text or code formatting")
	}
	longLine := strings.Repeat("🙂", 4500)
	parts = formatReply("`" + longLine + "`")
	all.Reset()
	for _, part := range parts {
		if len(part.Entities) != 1 || part.Entities[0].Type != "code" || entityText(t, part, part.Entities[0]) != part.Text {
			t.Fatal("long inline code lost formatting")
		}
		all.WriteString(part.Text)
	}
	if all.String() != longLine {
		t.Fatal("long single line lost text")
	}
}
