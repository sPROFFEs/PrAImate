package core

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedGitCommandsInheritIdentityWithoutForwardingUserEnvironment(t *testing.T) {
	for _, source := range []string{"global", "repository", "fallback"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			config := filepath.Join(t.TempDir(), "gitconfig")
			body := ""
			name, email := "Fixture User", "fixture@example.invalid"
			if source == "global" {
				body = "[user]\nname = Fixture User\nemail = fixture@example.invalid\n"
			}
			if err := os.WriteFile(config, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_CONFIG_GLOBAL", config)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
				t.Setenv(key, "")
			}
			if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil {
				t.Fatalf("init: %s %v", out, err)
			}
			if source == "repository" {
				for _, setting := range [][2]string{{"user.name", name}, {"user.email", email}} {
					if out, err := exec.Command("git", "-C", root, "config", setting[0], setting[1]).CombinedOutput(); err != nil {
						t.Fatalf("config: %s %v", out, err)
					}
				}
			}
			if source == "fallback" {
				name, email = "PrAImate Workers", "workers@praimate.local"
			}
			broker, err := newManagedToolBroker(context.Background(), nil, AgentCapabilities{ExecuteCommands: true}, root, &ApprovalConfig{Request: func(context.Context, string, map[string]any) (bool, error) { return true, nil }}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer broker.Close()
			for _, args := range [][]string{{"-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "test"}, {"log", "-1", "--format=%an <%ae>"}} {
				raw, _ := json.Marshal(map[string]any{"command": "git", "args": args})
				out, err := broker.ExecuteTool(context.Background(), "command.run", raw)
				if err != nil {
					t.Fatalf("git %v: %v", args, err)
				}
				if args[0] == "log" && !strings.Contains(out, name+" <"+email+">") {
					t.Fatalf("identity not inherited: %q", out)
				}
			}
		})
	}
}
