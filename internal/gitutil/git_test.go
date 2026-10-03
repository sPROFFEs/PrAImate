package gitutil

import (
	"context"
	"os/exec"
	"reflect"
	"testing"
)

func TestOriginProbeKeepsInternalHostBehavior(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	ctx, dir := context.Background(), t.TempDir()
	if out, err := exec.CommandContext(ctx, "git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("init fixture: %v: %s", err, out)
	}
	previous := internalGitHost
	internalGitHost = "git.internal.example"
	t.Cleanup(func() { internalGitHost = previous })
	for _, tc := range []struct {
		remote string
		bypass bool
	}{
		{"https://git.internal.example/example/backup.git", true},
		{"https://github.com/example/backup.git", false},
	} {
		if out, err := exec.CommandContext(ctx, "git", "-C", dir, "config", "remote.origin.url", tc.remote).CombinedOutput(); err != nil {
			t.Fatalf("configure fixture: %v: %s", err, out)
		}
		args := []string{"fetch", "origin"}
		want := append([]string(nil), args...)
		if tc.bypass {
			want = append([]string{"-c", "http.sslVerify=false"}, want...)
		}
		if got := DisableSSLVerifyForInternalHostOrOrigin(ctx, dir, args...); !reflect.DeepEqual(got, want) {
			t.Fatalf("origin detection changed: got %v, want %v", got, want)
		}
	}
}
