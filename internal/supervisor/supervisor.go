// Package supervisor runs Gotenberg as a supervised child process.
package supervisor

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/dockette/docshot/internal/metrics"
)

// Supervisor runs Gotenberg as a child, pinned to loopback so the one public port is ours.
type Supervisor struct {
	args []string
	pid  atomic.Int64 // the running Gotenberg; its exit status belongs to cmd.Wait
}

func New(args []string, port string) *Supervisor {
	a := append([]string{}, args...)
	a = append(a, "--api-bind-ip=127.0.0.1", "--api-port="+port)
	return &Supervisor{args: a}
}

// Run keeps Gotenberg alive until ctx is cancelled, restarting with backoff; it returns once Gotenberg is gone.
func (s *Supervisor) Run(ctx context.Context) {
	// Child subreaper: Gotenberg's orphaned LibreOffice/Chromium re-parent to us, not tini,
	// so we can kill them instead of leaking a soffice.bin per crash.
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0); errno != 0 {
		slog.Warn("PR_SET_CHILD_SUBREAPER failed; orphans of a crashed Gotenberg may leak", "errno", errno)
	}
	go s.reapZombies(ctx)

	backoff := time.Second
	for {
		started := time.Now()
		cmd := exec.Command(s.args[0], s.args[1:]...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		// Own process group: Chromium and LibreOffice die with Gotenberg, and are told apart from our own tools.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		slog.Info("starting gotenberg", "cmd", strings.Join(s.args, " "))
		if err := cmd.Start(); err != nil {
			slog.Error("gotenberg failed to start", "err", err)
		} else {
			s.pid.Store(int64(cmd.Process.Pid))
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-ctx.Done():
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
				select {
				case <-done:
				case <-time.After(20 * time.Second):
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					<-done
				}
				s.pid.Store(0)
				killOrphans()
				return
			case err := <-done:
				s.pid.Store(0)
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				n := killOrphans()
				metrics.GotenbergRestarts.Inc()
				slog.Error("gotenberg exited, restarting", "err", err, "orphans_killed", n)
			}
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

const prSetChildSubreaper = 36 // PR_SET_CHILD_SUBREAPER, linux/prctl.h

type proc struct {
	state string
	pgid  int
}

// foreignChildren lists our direct children from /proc that are not in our process group: pid → proc.
// Gotenberg and whatever it orphans live outside our group (Setpgid, and Chromium/LibreOffice may make
// their own); the tools docshot runs itself (ffmpeg, bsdtar…) stay in it and are waited for by os/exec,
// so they are never killed or reaped here.
func foreignChildren() map[int]proc {
	self, group := strconv.Itoa(os.Getpid()), syscall.Getpgrp()
	out := map[int]proc{}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// pid (comm) state ppid pgrp … — comm may contain spaces, so split after ')'.
		rest := string(stat)
		if i := strings.LastIndexByte(rest, ')'); i >= 0 {
			rest = rest[i+2:]
		}
		f := strings.Fields(rest)
		if len(f) < 3 || f[1] != self {
			continue
		}
		if pgid, err := strconv.Atoi(f[2]); err == nil && pgid != group {
			out[pid] = proc{state: f[0], pgid: pgid}
		}
	}
	return out
}

// killOrphans kills and reaps Gotenberg's leftovers; loops because kills can orphan more onto us.
// Call it only once Gotenberg itself has been waited for.
func killOrphans() int {
	n := 0
	for round := 0; round < 10; round++ {
		kids := foreignChildren()
		if len(kids) == 0 {
			break
		}
		for pid := range kids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			var ws syscall.WaitStatus
			_, _ = syscall.Wait4(pid, &ws, 0, nil)
			n++
		}
	}
	return n
}

// reapZombies reaps re-parented zombies while Gotenberg runs.
func (s *Supervisor) reapZombies(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		reapOnce(int(s.pid.Load()))
	}
}

// reapOnce reaps foreign zombies except gotenberg (its status belongs to cmd.Wait); it returns how many.
func reapOnce(gotenberg int) int {
	n := 0
	for pid, p := range foreignChildren() {
		if p.state == "Z" && pid != gotenberg {
			var ws syscall.WaitStatus
			if got, _ := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); got == pid {
				n++
			}
		}
	}
	return n
}
