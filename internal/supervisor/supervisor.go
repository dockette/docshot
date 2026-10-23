// Package supervisor runs Gotenberg as a supervised child process.
package supervisor

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dockette/docshot/internal/metrics"
)

// Supervisor runs Gotenberg as a child, pinned to loopback so the one public port is ours.
type Supervisor struct {
	args []string
}

func New(args []string, port string) *Supervisor {
	a := append([]string{}, args...)
	a = append(a, "--api-bind-ip=127.0.0.1", "--api-port="+port)
	return &Supervisor{args: a}
}

// Run keeps Gotenberg alive until ctx is cancelled, restarting with backoff.
func (s *Supervisor) Run(ctx context.Context) {
	// Child subreaper: Gotenberg's orphaned LibreOffice/Chromium re-parent to us, not tini,
	// so we can kill them instead of leaking a soffice.bin per crash.
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0); errno != 0 {
		slog.Warn("PR_SET_CHILD_SUBREAPER failed; orphans of a crashed Gotenberg may leak", "errno", errno)
	}
	go reapZombies(ctx)

	backoff := time.Second
	for {
		started := time.Now()
		cmd := exec.Command(s.args[0], s.args[1:]...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		// Own process group: Chromium and LibreOffice die with Gotenberg.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		slog.Info("starting gotenberg", "cmd", strings.Join(s.args, " "))
		if err := cmd.Start(); err != nil {
			slog.Error("gotenberg failed to start", "err", err)
		} else {
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-ctx.Done():
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
				select {
				case <-done:
				case <-time.After(20 * time.Second):
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				}
				killOrphans()
				return
			case err := <-done:
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

// children lists our direct children from /proc: pid → state.
func children() map[int]string {
	self := os.Getpid()
	out := map[int]string{}
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
		// pid (comm) state ppid … — comm may contain spaces, so split after ')'.
		rest := string(stat)
		if i := strings.LastIndexByte(rest, ')'); i >= 0 {
			rest = rest[i+2:]
		}
		f := strings.Fields(rest)
		if len(f) > 1 && f[1] == strconv.Itoa(self) {
			out[pid] = f[0]
		}
	}
	return out
}

// killOrphans kills and reaps leftover children; loops because kills can orphan more onto us.
func killOrphans() int {
	n := 0
	for round := 0; round < 10; round++ {
		kids := children()
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

// reapZombies reaps re-parented zombies while Gotenberg runs; it never waits on Gotenberg (cmd.Wait owns its status).
func reapZombies(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for pid, state := range children() {
			if state == "Z" && !isGotenberg(pid) {
				var ws syscall.WaitStatus
				_, _ = syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
			}
		}
	}
}

func isGotenberg(pid int) bool {
	comm, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	return strings.TrimSpace(string(comm)) == "gotenberg"
}
