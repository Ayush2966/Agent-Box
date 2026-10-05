// Package proc supervises long-running dev-server processes and captures their output.
package proc

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type Spec struct {
	Command string // run with sh -c
	Dir     string
	Env     []string
	Out     io.Writer // receives stdout and stderr; nil discards
}

type Options struct {
	Backoff     []time.Duration // delay before restart N (last value repeats)
	CrashWindow time.Duration
	MaxCrashes  int // give up after this many crashes within CrashWindow
	StopTimeout time.Duration
}

var DefaultOptions = Options{
	Backoff:     []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second},
	CrashWindow: time.Minute,
	MaxCrashes:  5,
	StopTimeout: 10 * time.Second,
}

type Status struct {
	Running   bool
	PID       int
	Restarts  int
	LastError string
	GaveUp    bool
}

// Proc runs one command, restarting it when it exits unexpectedly. A Proc is single use.
type Proc struct {
	spec     Spec
	opts     Options
	onChange func(Status)

	mu       sync.Mutex
	status   Status
	started  bool
	stopping bool
	stop     chan struct{}
	done     chan struct{}
}

func New(spec Spec, opts Options, onChange func(Status)) *Proc {
	if onChange == nil {
		onChange = func(Status) {}
	}
	return &Proc{spec: spec, opts: opts, onChange: onChange}
}

func (p *Proc) Start() error {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return errors.New("process already started")
	}
	p.started = true
	p.stop = make(chan struct{})
	p.done = make(chan struct{})
	cmd, err := p.spawnLocked()
	if err != nil {
		close(p.done)
		p.mu.Unlock()
		return err
	}
	st := p.status
	p.mu.Unlock()
	p.onChange(st)
	go p.supervise(cmd)
	return nil
}

func (p *Proc) spawnLocked() (*exec.Cmd, error) {
	cmd := exec.Command("sh", "-c", p.spec.Command)
	cmd.Dir = p.spec.Dir
	cmd.Env = p.spec.Env
	cmd.Stdout = p.spec.Out
	cmd.Stderr = p.spec.Out
	cmd.SysProcAttr = sysProcAttr()
	cmd.WaitDelay = 2 * time.Second // don't hang on pipes held open by orphaned children
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %q: %w", p.spec.Command, err)
	}
	p.status.Running = true
	p.status.PID = cmd.Process.Pid
	return cmd, nil
}

func (p *Proc) supervise(cmd *exec.Cmd) {
	defer close(p.done)
	var crashes []time.Time
	for {
		waitErr := cmd.Wait()
		killGroup(cmd.Process.Pid, syscall.SIGKILL) // reap anything the command left behind

		p.mu.Lock()
		p.status.Running, p.status.PID = false, 0
		if p.stopping {
			st := p.status
			p.mu.Unlock()
			p.onChange(st)
			return
		}
		now := time.Now()
		crashes = append(crashes, now)
		for len(crashes) > 0 && now.Sub(crashes[0]) > p.opts.CrashWindow {
			crashes = crashes[1:]
		}
		reason := "exited with status 0"
		if waitErr != nil {
			reason = waitErr.Error()
		}
		p.status.LastError = reason
		if len(crashes) >= p.opts.MaxCrashes {
			p.status.GaveUp = true
			p.status.LastError = fmt.Sprintf("crashed %d times within %s (last: %s); not restarting", len(crashes), p.opts.CrashWindow, reason)
			st := p.status
			p.mu.Unlock()
			p.onChange(st)
			return
		}
		delay := p.opts.Backoff[min(len(crashes), len(p.opts.Backoff))-1]
		st := p.status
		p.mu.Unlock()
		p.onChange(st)

		select {
		case <-p.stop:
			return
		case <-time.After(delay):
		}

		p.mu.Lock()
		if p.stopping {
			p.mu.Unlock()
			return
		}
		next, err := p.spawnLocked()
		if err != nil {
			p.status.GaveUp, p.status.LastError = true, err.Error()
			st := p.status
			p.mu.Unlock()
			p.onChange(st)
			return
		}
		p.status.Restarts++
		st = p.status
		p.mu.Unlock()
		p.onChange(st)
		cmd = next
	}
}

// Stop sends SIGTERM to the process group, then SIGKILL after StopTimeout.
func (p *Proc) Stop() error {
	p.mu.Lock()
	if !p.started {
		p.mu.Unlock()
		return nil
	}
	done := p.done
	if !p.stopping {
		p.stopping = true
		close(p.stop)
	}
	pid := 0
	if p.status.Running {
		pid = p.status.PID
	}
	p.mu.Unlock()

	killGroup(pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(p.opts.StopTimeout):
		killGroup(pid, syscall.SIGKILL)
		<-done
	}
	return nil
}

func (p *Proc) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func killGroup(pid int, sig syscall.Signal) {
	if pid > 0 {
		_ = syscall.Kill(-pid, sig)
	}
}
