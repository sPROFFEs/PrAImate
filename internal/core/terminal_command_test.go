package core

import (
	"reflect"
	"testing"
)

func TestInteractiveCLICommand(t *testing.T) {
	for _, cli := range []string{"claude", "openclaude", "codex", "opencode", "praimate-code"} {
		t.Run(cli, func(t *testing.T) {
			name, args, err := InteractiveCLICommand(cli, "")
			if err != nil || name != cli || len(args) != 0 {
				t.Fatalf("default launch: %q %q %v", name, args, err)
			}
			model := "provider/model with spaces; literal-not-a-shell-command"
			name, args, err = InteractiveCLICommand(cli, model)
			flag := "--model"
			if cli == "codex" {
				flag = "-m"
			}
			if err != nil || name != cli || !reflect.DeepEqual(args, []string{flag, model}) {
				t.Fatalf("model not kept as one argv value: %q %q %v", name, args, err)
			}
		})
	}
	for _, cli := range []string{"", "sh", "powershell", "codex;echo injected"} {
		if _, _, err := InteractiveCLICommand(cli, ""); err == nil {
			t.Fatalf("accepted unknown CLI %q", cli)
		}
	}
	for _, model := range []string{"--help", "a\nb", "a\rb", "a\x00b"} {
		if _, _, err := InteractiveCLICommand("claude", model); err == nil {
			t.Fatalf("accepted invalid model %q", model)
		}
	}
}
