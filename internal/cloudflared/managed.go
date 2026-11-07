package cloudflared

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// PrepareManaged installs only the connector executable. Its lifetime will be
// owned by the headless Server, including before the user's graphical login.
func (m *Manager) PrepareManaged(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.prepareManaged(ctx)
}
func (m *Manager) prepareManaged(ctx context.Context) error {
	c, err := m.configuration(ctx)
	if err != nil {
		return err
	}
	if _, err = NormalizeURL(c.PublicURL); err != nil {
		return err
	}
	if err = validateToken(c.Token); err != nil {
		return err
	}
	if _, installed := m.installed(); installed {
		return errors.New("Remove this profile's separate Cloudflare connector service before enabling Server-managed tunnel startup; its saved tunnel configuration is retained")
	}
	if err = privateDirectory(m.root); err != nil {
		return err
	}
	if info, err := os.Lstat(m.root); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Managed connector directory must be a real private directory")
	}
	if err = privateWrite(filepath.Join(m.root, ".gitignore"), []byte("*\n")); err != nil {
		return err
	}
	if _, err = os.Stat(m.binary()); errors.Is(err, os.ErrNotExist) {
		return downloadBinary(ctx, m.binary())
	}
	return err
}
func (m *Manager) StartManaged(parent context.Context, output io.Writer) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.managedCancel != nil {
		return nil, errors.New("A Server-managed connector is already running")
	}
	if err := m.prepareManaged(parent); err != nil {
		return nil, err
	}
	c, err := m.configuration(parent)
	if err != nil {
		return nil, err
	}
	token := filepath.Join(m.root, "token")
	if err = privateWrite(token, []byte(c.Token)); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	command := func() *exec.Cmd {
		cmd := exec.CommandContext(ctx, m.binary(), "tunnel", "--no-autoupdate", "--metrics", fmt.Sprintf("127.0.0.1:%d", port), "run", "--token-file", token)
		hideWindow(cmd)
		cmd.Stdout = output
		cmd.Stderr = output
		return cmd
	}
	cmd := command()
	if err = cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("Start managed Cloudflare connector: %w", err)
	}
	m.managedCancel = cancel
	m.managedPort = port
	go func() {
		defer close(done)
		defer func() { m.mu.Lock(); m.managedCancel = nil; m.managedPort = 0; m.mu.Unlock() }()
		for {
			_ = cmd.Wait()
			if ctx.Err() != nil {
				return
			}
			fmt.Fprintln(output, "Cloudflare connector exited; retrying in five seconds")
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			cmd = command()
			if err := cmd.Start(); err != nil {
				fmt.Fprintln(output, "Cloudflare connector could not restart")
				continue
			}
		}
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}, nil
}
