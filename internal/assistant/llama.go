package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LlamaProvider owns only its explicitly selected verified runtime and model.
// It never discovers/downloads models or launches llama.cpp's agent tools.
type LlamaProvider struct {
	mu                 sync.Mutex
	Runtime, ModelPath string
	Config             Config
	cmd                *exec.Cmd
	done               chan struct{}
	http               *HTTPProvider
	idle               *time.Timer
	idleEpoch          uint64
	Usage              func(int, int)
	BeforeStart        func(context.Context) error
}

func (p *LlamaProvider) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.idleEpoch++
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.idle != nil {
		p.idle.Stop()
		p.idle = nil
	}
	if p.cmd != nil {
		select {
		case <-p.done:
			p.cmd = nil
			p.http = nil
		default:
			return nil
		}
	}
	if p.BeforeStart != nil {
		if err := p.BeforeStart(ctx); err != nil {
			return err
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	key := hex.EncodeToString(token[:])
	args := []string{"-m", p.ModelPath, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "-c", strconv.Itoa(p.Config.Context), "-t", strconv.Itoa(p.Config.Threads), "-ngl", strconv.Itoa(p.Config.GPULayers), "-b", strconv.Itoa(p.Config.Batch), "--parallel", "1", "--alias", "assistant"}
	cmd := exec.Command(p.Runtime, args...)
	cmd.Dir = filepath.Dir(p.Runtime)
	if err := prepareRuntimeCommand(cmd); err != nil {
		return err
	}
	var diagnostic runtimeDiagnostic
	cmd.Stdout = &diagnostic
	cmd.Stderr = &diagnostic
	// Authentication stays outside argv/logs and is never part of model context.
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "LLAMA_API_KEY=") && !strings.HasPrefix(env, "LLAMA_ARG_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "LLAMA_API_KEY="+key)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start managed Assistant runtime: %w", err)
	}
	p.cmd = cmd
	p.done = make(chan struct{})
	done := p.done
	go func() { _ = cmd.Wait(); close(done) }()
	p.http = &HTTPProvider{Endpoint: fmt.Sprintf("http://127.0.0.1:%d/v1", port), Model: "assistant", APIKey: key, Managed: true, Usage: p.Usage}
	readyCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	var lastHealth error
	for {
		select {
		case <-done:
			p.cmd = nil
			p.http = nil
			return fmt.Errorf("managed Assistant runtime exited before becoming ready (%s)%s", runtimeExitDescription(cmd.ProcessState.ExitCode()), diagnostic.detail(key))
		case <-readyCtx.Done():
			_ = cmd.Process.Kill()
			<-done
			p.cmd = nil
			p.http = nil
			return fmt.Errorf("managed Assistant runtime did not become ready: %w; last health check: %v%s", readyCtx.Err(), lastHealth, diagnostic.detail(key))
		case <-ticker.C:
			probeCtx, cancel := context.WithTimeout(readyCtx, time.Second)
			err := p.http.Health(probeCtx)
			cancel()
			lastHealth = err
			if err == nil {
				return nil
			}
		}
	}
}
func (p *LlamaProvider) Stop(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.idleEpoch++
	return p.stopLocked(ctx)
}
func (p *LlamaProvider) stopLocked(ctx context.Context) error {
	if p.idle != nil {
		p.idle.Stop()
		p.idle = nil
	}
	if p.cmd == nil {
		return nil
	}
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
		p.cmd = nil
		p.http = nil
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *LlamaProvider) Health(ctx context.Context) error {
	p.mu.Lock()
	http := p.http
	p.mu.Unlock()
	if http == nil {
		return errors.New("Assistant model is not loaded")
	}
	if err := http.Health(ctx); err != nil {
		return err
	}
	p.armIdle()
	return nil
}
func (p *LlamaProvider) armIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.idleEpoch++
	epoch := p.idleEpoch
	if p.idle != nil {
		p.idle.Stop()
	}
	if !p.Config.KeepLoaded && p.cmd != nil {
		p.idle = time.AfterFunc(time.Duration(p.Config.IdleSeconds)*time.Second, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if epoch != p.idleEpoch {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = p.stopLocked(ctx)
		})
	}
}
func (p *LlamaProvider) Generate(ctx context.Context, r Request) (string, error) {
	if err := p.Start(ctx); err != nil {
		return "", err
	}
	p.mu.Lock()
	http := p.http
	p.mu.Unlock()
	if http == nil {
		return "", errors.New("Assistant runtime stopped before inference")
	}
	result, err := http.Generate(ctx, r)
	p.armIdle()
	return result, err
}
