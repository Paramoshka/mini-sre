package telegram

import "strings"

const maxMessageEntities = 100

type messageEntity struct {
	Type     string `json:"type"`
	Offset   int    `json:"offset"`
	Length   int    `json:"length"`
	Language string `json:"language,omitempty"`
}

type messagePart struct {
	Text     string          `json:"text"`
	Entities []messageEntity `json:"entities,omitempty"`
}

type replyFormatter struct {
	text     strings.Builder
	units    int
	entities []messageEntity
}

func formatReply(text string) []messagePart {
	var f replyFormatter
	lines := strings.SplitAfter(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "```") || strings.Contains(line[3:], "`") {
			f.inline(lines[i], false)
			continue
		}
		end := i + 1
		for end < len(lines) && strings.TrimSpace(lines[end]) != "```" {
			end++
		}
		if end == len(lines) {
			// Preserve incomplete fences literally, including any shell metacharacters.
			f.append(strings.Join(lines[i:], ""), "", "")
			break
		}
		f.append(strings.Join(lines[i+1:end], ""), "pre", strings.TrimSpace(line[3:]))
		i = end
	}
	if strings.TrimSpace(f.text.String()) == "" {
		return splitReply(messagePart{Text: text})
	}
	return splitReply(messagePart{Text: f.text.String(), Entities: f.entities})
}

func (f *replyFormatter) append(text, kind, language string) {
	length := utf16Length(text)
	if kind != "" && length > 0 {
		f.entities = append(f.entities, messageEntity{Type: kind, Offset: f.units, Length: length, Language: language})
	}
	f.text.WriteString(text)
	f.units += length
}

func (f *replyFormatter) inline(text string, bold bool) {
	kind := ""
	if bold {
		kind = "bold"
	}
	for text != "" {
		if text[0] == '`' {
			n := len(text) - len(strings.TrimLeft(text, "`"))
			marker := text[:n]
			if end := strings.Index(text[n:], marker); n < 3 && end > 0 {
				f.append(text[n:n+end], "code", "")
				text = text[n+end+n:]
				continue
			}
			f.append(marker, kind, "")
			text = text[n:]
			continue
		}
		if !bold && strings.HasPrefix(text, "**") {
			if end := strings.Index(text[2:], "**"); end > 0 {
				// Telegram forbids overlapping bold/code entities; format each text span separately.
				f.inline(text[2:2+end], true)
				text = text[2+end+2:]
				continue
			}
		}
		end := strings.IndexAny(text, "`*")
		if end < 0 {
			end = len(text)
		} else if end == 0 {
			end = 1
		}
		f.append(text[:end], kind, "")
		text = text[end:]
	}
}

func splitReply(reply messagePart) []messagePart {
	var parts []messagePart
	start := 0
	for _, chunk := range splitText(reply.Text) {
		for chunk != "" {
			part := messagePart{Text: chunk}
			end := start + utf16Length(chunk)
			for _, entity := range reply.Entities {
				left := max(start, entity.Offset)
				right := min(end, entity.Offset+entity.Length)
				if left >= right {
					continue
				}
				if len(part.Entities) == maxMessageEntities {
					end = left
					part.Text = chunk[:utf16ByteIndex(chunk, end-start)]
					break
				}
				entity.Offset, entity.Length = left-start, right-left
				part.Entities = append(part.Entities, entity)
			}
			parts = append(parts, part)
			start = end
			chunk = chunk[len(part.Text):]
		}
	}
	return parts
}

func utf16ByteIndex(text string, offset int) int {
	units := 0
	for i, r := range text {
		if units == offset {
			return i
		}
		units++
		if r > 0xffff {
			units++
		}
	}
	return len(text)
}

func utf16Length(text string) int {
	units := 0
	for _, r := range text {
		units++
		if r > 0xffff {
			units++
		}
	}
	return units
}

func splitText(text string) []string {
	var parts []string
	start, units, newline := 0, 0, -1
	for i, r := range text {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if units+n > 4000 {
			end := i
			if newline > start {
				end = newline
			}
			parts = append(parts, text[start:end])
			start, units, newline = end, utf16Length(text[end:i]), -1
		}
		units += n
		if r == '\n' {
			newline = i + 1
		}
	}
	if start < len(text) {
		parts = append(parts, text[start:])
	}
	return parts
}
