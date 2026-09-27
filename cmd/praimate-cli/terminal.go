package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"
)

var errInputCanceled = errors.New("input cancelled")

const interactiveHelp = `Commands:
  /help                 Show this help
  /status, /context     Session settings / context budget and last usage
  /models, /model ID    List endpoint models / select a model
  /tools LEVEL          safe | ask | edits | full
  /attach PATH          Queue one file (spaces and quoted paths supported)
  /attachments          List files queued for the next message
  /detach N|all         Remove a queued file (1-based) or all files
  /mcp, /skills         Show configured core tools and skills
  /sessions             List native core chats
  /compact              Compact model history (lossy; drops old images)
  /clear                Start a new chat, keeping the previous transcript
  /exit                 Exit (also Ctrl+D on an empty prompt)
Keys: arrows/history, Home/End, Tab command completion.
End a line with \ for multiline input. Bracketed paste keeps newlines;
press Enter after pasting. Pasted slash commands are sent as text.
Ctrl+C cancels the current turn or clears the current input.
Images go to the selected endpoint; they are not text-redacted.`

var slashCommands = []string{"/help", "/status", "/context", "/models", "/model", "/tools", "/attach", "/attachments", "/detach", "/mcp", "/skills", "/sessions", "/compact", "/clear", "/exit"}

func completeCommand(line string, pos int, key rune) (string, int, bool) {
	if key != '\t' || pos != len(line) || !strings.HasPrefix(line, "/") || strings.ContainsAny(line, " \t\n") {
		return "", 0, false
	}
	var matches []string
	for _, command := range slashCommands {
		if strings.HasPrefix(command, line) {
			matches = append(matches, command)
		}
	}
	if len(matches) == 0 {
		return line, pos, true
	}
	prefix := matches[0]
	for _, match := range matches[1:] {
		for !strings.HasPrefix(match, prefix) {
			prefix = prefix[:len(prefix)-1]
		}
	}
	if len(matches) == 1 {
		prefix += " "
	}
	return prefix, len(prefix), true
}

// Prompts live only in memory; approvals are never added to history.
type terminalHistory []string

func (h *terminalHistory) Add(string)      {} // ReadLine adds fragments of a paste; record only completed prompts ourselves.
func (h *terminalHistory) Len() int        { return len(*h) }
func (h *terminalHistory) At(i int) string { return (*h)[len(*h)-1-i] }
func (h *terminalHistory) remember(line string) {
	if strings.TrimSpace(line) == "" || (len(*h) > 0 && h.At(0) == line) {
		return
	}
	*h = append(*h, line)
	if len(*h) > 100 {
		*h = append(terminalHistory(nil), (*h)[len(*h)-100:]...)
	}
}

type inputByte struct {
	value byte
	err   error
}

// One reader owns stdin for the entire interactive session. Cancelling an
// approval never leaves an abandoned goroutine competing with the next prompt.
// Input typed while the model is busy is discarded, not queued as an approval.
type terminalInput struct {
	mu     sync.Mutex
	active *promptInput
	done   chan struct{}
	ended  chan struct{}
	err    error
}

func newTerminalInput(reader *bufio.Reader) *terminalInput {
	in := &terminalInput{done: make(chan struct{}), ended: make(chan struct{})}
	go func() {
		defer close(in.ended)
		for {
			b, err := reader.ReadByte()
			in.mu.Lock()
			active := in.active
			if err != nil {
				in.err = err
			}
			in.mu.Unlock()
			if active != nil {
				select {
				case active.keys <- inputByte{b, err}:
				case <-active.done:
				case <-in.done:
					return
				}
			}
			if err != nil {
				return
			}
			select {
			case <-in.done:
				return
			default:
			}
		}
	}()
	return in
}

type promptInput struct {
	owner    *terminalInput
	ctx      context.Context
	keys     chan inputByte
	done     chan struct{}
	last     byte
	sequence string
	pasted   bool
}

func (in *terminalInput) begin(ctx context.Context) *promptInput {
	reader := &promptInput{owner: in, ctx: ctx, keys: make(chan inputByte, 4096), done: make(chan struct{})}
	in.mu.Lock()
	in.active = reader
	in.mu.Unlock()
	return reader
}
func (r *promptInput) close() {
	r.owner.mu.Lock()
	if r.owner.active == r {
		r.owner.active = nil
	}
	close(r.done)
	r.owner.mu.Unlock()
}
func (r *promptInput) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	var key inputByte
	select {
	case key = <-r.keys:
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	case <-r.owner.ended:
		select {
		case key = <-r.keys:
		default:
			r.owner.mu.Lock()
			err := r.owner.err
			r.owner.mu.Unlock()
			return 0, err
		}
	}
	if key.err != nil {
		return 0, key.err
	}
	r.last, p[0] = key.value, key.value
	r.sequence += string(key.value)
	if len(r.sequence) > 6 {
		r.sequence = r.sequence[len(r.sequence)-6:]
	}
	if r.sequence == "\x1b[200~" {
		r.pasted = true
	}
	return 1, nil
}

type terminalEditor struct {
	input   *terminalInput
	history terminalHistory
	readMu  sync.Mutex
}

func (e *terminalEditor) read(ctx context.Context, prompt string, approval bool) (string, bool, error) {
	e.readMu.Lock()
	defer e.readMu.Unlock()
	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return "", false, err
	}
	defer term.Restore(int(os.Stdin.Fd()), state)
	r := e.input.begin(ctx)
	defer r.close()
	editor := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{r, os.Stderr}, prompt)
	if width, height, err := term.GetSize(int(os.Stderr.Fd())); err == nil {
		_ = editor.SetSize(width, height)
	}
	if !approval {
		editor.History = &e.history
		editor.AutoCompleteCallback = completeCommand
	}
	editor.SetBracketedPasteMode(true)
	defer editor.SetBracketedPasteMode(false)
	line, pasted, err := readEditedInput(editor, r, approval)
	if err != nil {
		fmt.Fprint(os.Stderr, "\r\n")
		return "", pasted, err
	}
	if !approval {
		e.history.remember(line)
	}
	return line, pasted, nil
}

func readEditedInput(editor *term.Terminal, reader *promptInput, approval bool) (string, bool, error) {
	var lines []string
	size := 0
	for {
		line, err := editor.ReadLine()
		if errors.Is(err, io.EOF) && reader.last == 3 {
			return "", reader.pasted, errInputCanceled
		}
		if err != nil && !errors.Is(err, term.ErrPasteIndicator) {
			return "", reader.pasted, err
		}
		if len([]rune(line)) >= 4096 {
			return "", reader.pasted, errors.New("an edited line must be shorter than 4096 characters; use multiline input, --attach, or piped stdin for larger content")
		}
		size += len(line) + 1
		if size > 1<<20 {
			return "", reader.pasted, errors.New("prompt exceeds 1 MiB")
		}
		if errors.Is(err, term.ErrPasteIndicator) {
			lines = append(lines, line)
			editor.SetPrompt("... ")
			continue
		}
		if !approval && !reader.pasted && strings.HasSuffix(line, "\\") {
			lines = append(lines, strings.TrimSuffix(line, "\\"))
			editor.SetPrompt("... ")
			continue
		}
		lines = append(lines, line)
		return strings.Join(lines, "\n"), reader.pasted || len(lines) > 1, nil
	}
}

// Model/tool output is data, not terminal escape sequences (OSC clipboard,
// hyperlinks, cursor movement, etc.). JSONL remains unmodified machine data.
type safeTerminalWriter struct{ target io.Writer }

func (w safeTerminalWriter) Write(p []byte) (int, error) {
	text := strings.Map(func(r rune) rune {
		if (r < 32 && r != '\n' && r != '\t') || (r >= 0x7f && r <= 0x9f) {
			return -1
		}
		return r
	}, string(p))
	_, err := io.WriteString(w.target, text)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}
