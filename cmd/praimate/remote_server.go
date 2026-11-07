package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/sPROFFEs/PrAImate/internal/version"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

func runRemoteServer(args []string) int {
	if version.Edition != "server" {
		fmt.Fprintln(os.Stderr, "Remote service requires the PrAImate server edition")
		return 2
	}
	exe, _ := os.Executable()
	bin := resolveGUIBinary(exe)
	if bin == "" {
		fmt.Fprintln(os.Stderr, "Server backend not found next to praimate")
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, append([]string{"--server"}, args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.WaitDelay = 10 * time.Second
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return 0
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
