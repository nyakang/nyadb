package agentbridge

import (
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processOwner struct {
	mu  sync.Mutex
	job windows.Handle
}

func prepareProcess(cmd *exec.Cmd) (*processOwner, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	return &processOwner{job: job}, nil
}
func (p *processOwner) attach(cmd *exec.Cmd) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job == 0 {
		return ErrLost
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	return windows.AssignProcessToJobObject(p.job, handle)
}
func (p *processOwner) kill(cmd *exec.Cmd) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job != 0 {
		_ = windows.TerminateJobObject(p.job, 1)
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
func (p *processOwner) release(*exec.Cmd) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job != 0 {
		_ = windows.CloseHandle(p.job)
		p.job = 0
	}
}
