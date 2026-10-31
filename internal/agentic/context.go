package agentic

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

type contextEntry struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type contextManager struct {
	maxChars int
	entries  []contextEntry
}

func (m *contextManager) add(role, text string) {
	text = strings.TrimSpace(text)
	if text != "" {
		m.entries = append(m.entries, contextEntry{Role: role, Text: text})
	}
}

func (m *contextManager) render(memory Memory) string {
	memoryText := memory.summary(m.maxChars / 3)
	header := "WORKING MEMORY\n" + memoryText + "\nRUNTIME TRANSCRIPT\n"
	remaining := m.maxChars - len(header)
	if remaining < 1000 {
		remaining = 1000
	}
	selected := make([]string, 0, len(m.entries))
	used := 0
	for i := len(m.entries) - 1; i >= 0; i-- {
		line := fmt.Sprintf("[%s]\n%s\n", m.entries[i].Role, m.entries[i].Text)
		if used+len(line) > remaining {
			break
		}
		selected = append(selected, line)
		used += len(line)
	}
	var b strings.Builder
	b.WriteString(header)
	if len(selected) < len(m.entries) {
		b.WriteString("… older transcript entries omitted; durable decisions remain in working memory …\n")
	}
	for i := len(selected) - 1; i >= 0; i-- {
		b.WriteString(selected[i])
	}
	return b.String()
}

func boundText(text string, max int) (preview string, truncated bool) {
	if len(text) <= max {
		return text, false
	}
	if max <= 0 {
		return "\n\n… output truncated; full content stored as an artifact …", true
	}
	for max > 0 && !utf8.RuneStart(text[max]) {
		max--
	}
	return text[:max] + "\n\n… output truncated; full content stored as an artifact …", true
}
