//go:build !windows

package agentbridge

import (
	"os/exec"
	"sync"
	"syscall"
)

type processOwner struct {
	mu     sync.Mutex
	exited bool
}

func prepareProcess(cmd *exec.Cmd) (*processOwner, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &processOwner{}, nil
}
func (p *processOwner) attach(*exec.Cmd) error { return nil }
func (p *processOwner) kill(cmd *exec.Cmd) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cmd.Process != nil {
		// This group was created for this captured process, never found by name.
		if !p.exited {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		_ = cmd.Process.Kill()
	}
}
func (p *processOwner) release(cmd *exec.Cmd) { p.mu.Lock(); p.exited = true; p.mu.Unlock() }
