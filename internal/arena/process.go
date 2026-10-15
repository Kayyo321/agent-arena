//go:build darwin || linux

package arena

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type Process struct {
	Cmd  *exec.Cmd
	done chan struct{}
	once sync.Once
}

func StartProcess(ctx context.Context, cmd *exec.Cmd) (*Process, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if e := cmd.Start(); e != nil {
		return nil, e
	}
	p := &Process{Cmd: cmd, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			p.Stop()
		case <-p.done:
		}
	}()
	return p, nil
}
func (p *Process) Stop() {
	p.once.Do(func() {
		_ = syscall.Kill(-p.Cmd.Process.Pid, syscall.SIGTERM)
		go func() {
			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			select {
			case <-timer.C:
				_ = syscall.Kill(-p.Cmd.Process.Pid, syscall.SIGKILL)
			case <-p.done: // A finished parent may still have descendants; stop its remaining group.
				_ = syscall.Kill(-p.Cmd.Process.Pid, syscall.SIGKILL)
			}
		}()
	})
}
func (p *Process) Wait() error {
	e := p.Cmd.Wait()
	close(p.done)
	_ = syscall.Kill(-p.Cmd.Process.Pid, syscall.SIGKILL)
	return e
}
func Interactive(ctx context.Context, cmd *exec.Cmd) error {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr // Interactive CLIs must retain the foreground process group for terminal input.
	if e := cmd.Start(); e != nil {
		return e
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Signal(os.Interrupt)
		case <-done:
		}
	}()
	return cmd.Wait()
}
func discardEnv(cmd *exec.Cmd) { cmd.Stderr = io.Discard }

func ProcessAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// Provider slots coordinate attached runs and manual invocations in this project.
func (p *Project) ProviderSlot(ctx context.Context, limit int) (*Lock, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		for n := 0; n < limit; n++ {
			l, err := p.Lock(fmt.Sprintf("provider-slot-%d", n))
			if err == nil {
				return l, nil
			}
			if !errors.Is(err, os.ErrExist) {
				return nil, err
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
