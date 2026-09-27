package main

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// textRenderer formats complete streamed lines. It never forwards model
// control characters; the only terminal escapes it emits are our own styles.
type textRenderer struct {
	out       io.Writer
	color     bool
	width     int
	line      strings.Builder
	maybeHead string
	table     [][]string
	inCode    bool
}

func newTextRenderer(out io.Writer, color bool, width int) *textRenderer {
	if width < 40 {
		width = 80
	}
	return &textRenderer{out: out, color: color, width: width}
}

func (r *textRenderer) WriteChunk(chunk string) {
	for _, char := range cleanTerminalText(chunk) {
		if char == '\n' {
			r.renderLine(strings.TrimSuffix(r.line.String(), "\r"))
			r.line.Reset()
		} else {
			r.line.WriteRune(char)
		}
	}
}

func (r *textRenderer) Flush() {
	if r.line.Len() > 0 {
		r.renderLine(r.line.String())
		r.line.Reset()
	}
	if r.maybeHead != "" {
		r.renderPlain(r.maybeHead)
		r.maybeHead = ""
	}
	r.flushTable()
}

func (r *textRenderer) renderLine(line string) {
	trimmed := strings.TrimSpace(line)
	if r.inCode {
		if strings.HasPrefix(trimmed, "```") {
			r.inCode = false
			fmt.Fprintln(r.out)
		} else {
			fmt.Fprintln(r.out, "  "+line)
		}
		return
	}
	if len(r.table) > 0 {
		if isPipeRow(trimmed) {
			r.table = append(r.table, splitPipeRow(trimmed))
			return
		}
		r.flushTable()
	}
	if r.maybeHead != "" {
		if isTableDivider(trimmed) {
			r.table = [][]string{splitPipeRow(r.maybeHead)}
			r.maybeHead = ""
			return
		}
		r.renderPlain(r.maybeHead)
		r.maybeHead = ""
	}
	if isPipeRow(trimmed) {
		r.maybeHead = trimmed
		return
	}
	r.renderPlain(line)
}

func (r *textRenderer) renderPlain(line string) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "```") {
		r.inCode = true
		lang := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
		label := "CODE"
		if lang != "" {
			label += " · " + lang
		}
		fmt.Fprintln(r.out, r.style("  "+label, "36"))
		return
	}
	if trimmed == "" {
		fmt.Fprintln(r.out)
		return
	}
	if heading := strings.TrimLeft(trimmed, "#"); len(heading) < len(trimmed) && strings.HasPrefix(heading, " ") {
		fmt.Fprintln(r.out, r.style(strings.ToUpper(strings.TrimSpace(heading)), "1;36"))
		return
	}
	if trimmed == "---" || trimmed == "***" {
		fmt.Fprintln(r.out, strings.Repeat("─", min(r.width, 64)))
		return
	}
	if strings.HasPrefix(trimmed, "> ") {
		fmt.Fprintln(r.out, r.style("│ ", "36")+r.inline(strings.TrimPrefix(trimmed, "> ")))
		return
	}
	if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		fmt.Fprintln(r.out, strings.Repeat(" ", indent)+"• "+r.inline(trimmed[2:]))
		return
	}
	fmt.Fprintln(r.out, r.inline(line))
}

func (r *textRenderer) inline(line string) string {
	var b strings.Builder
	bold, code := false, false
	for i := 0; i < len(line); {
		switch {
		case strings.HasPrefix(line[i:], "**"):
			bold = !bold
			if r.color {
				if bold {
					b.WriteString("\x1b[1m")
				} else {
					b.WriteString("\x1b[0m")
				}
			}
			i += 2
		case line[i] == '`':
			code = !code
			if r.color {
				if code {
					b.WriteString("\x1b[36m")
				} else {
					b.WriteString("\x1b[0m")
				}
			} else if code {
				b.WriteString("‹")
			} else {
				b.WriteString("›")
			}
			i++
		default:
			b.WriteByte(line[i])
			i++
		}
	}
	if r.color && (bold || code) {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

func (r *textRenderer) style(text, code string) string {
	if !r.color {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func isPipeRow(line string) bool {
	return strings.Contains(line, "|")
}

func splitPipeRow(line string) []string {
	line = strings.TrimPrefix(strings.TrimSuffix(line, "|"), "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func isTableDivider(line string) bool {
	if !isPipeRow(line) {
		return false
	}
	for _, cell := range splitPipeRow(line) {
		cell = strings.Trim(cell, " :")
		if len(cell) < 3 || strings.Trim(cell, "-") != "" {
			return false
		}
	}
	return true
}

func (r *textRenderer) flushTable() {
	if len(r.table) == 0 {
		return
	}
	columns := len(r.table[0])
	maxWidth := (r.width - columns*3 - 1) / columns
	if maxWidth > 36 {
		maxWidth = 36
	}
	if maxWidth < 8 {
		maxWidth = 8
	}
	widths := make([]int, columns)
	for _, row := range r.table {
		for i := 0; i < columns && i < len(row); i++ {
			widths[i] = max(widths[i], min(utf8.RuneCountInString(stripMarkup(row[i])), maxWidth))
		}
	}
	border := "+"
	for _, width := range widths {
		border += strings.Repeat("─", width+2) + "+"
	}
	fmt.Fprintln(r.out, border)
	for rowIndex, row := range r.table {
		line := "│"
		for i, width := range widths {
			cell := ""
			if i < len(row) {
				cell = truncateRunes(stripMarkup(row[i]), width)
			}
			line += " " + cell + strings.Repeat(" ", width-utf8.RuneCountInString(cell)) + " │"
		}
		fmt.Fprintln(r.out, line)
		if rowIndex == 0 {
			fmt.Fprintln(r.out, border)
		}
	}
	fmt.Fprintln(r.out, border)
	r.table = nil
}

func stripMarkup(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "**", ""), "`", "")
}

func truncateRunes(text string, width int) string {
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	return string(runes[:width-1]) + "…"
}
