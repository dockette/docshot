package supervisor

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// start runs a child; foreign ones get their own process group, like Gotenberg and its tree.
func start(t *testing.T, foreign bool, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	if foreign {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func state(pid int) string {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	rest := string(stat)
	return strings.Fields(rest[strings.LastIndexByte(rest, ')')+2:])[0]
}

func waitState(t *testing.T, pid int, want string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if state(pid) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d: state %q, want %q", pid, state(pid), want)
}

func TestKillOrphansSparesOwnTools(t *testing.T) {
	own := start(t, false, "sleep", "30")
	orphan := start(t, true, "sleep", "30")

	if n := killOrphans(); n != 1 {
		t.Fatalf("killed %d, want 1", n)
	}
	if state(orphan.Process.Pid) != "" {
		t.Fatal("foreign child survived")
	}
	if err := own.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("own tool was killed: %v", err)
	}
	_ = own.Process.Kill()
	// os/exec still owns the exit status: no ECHILD.
	if err := own.Wait(); err == nil || strings.Contains(err.Error(), "no child processes") {
		t.Fatalf("own Wait: %v, want killed", err)
	}
}

func TestReapOnce(t *testing.T) {
	own := start(t, false, "true")
	orphan := start(t, true, "true")
	gotenberg := start(t, true, "true")
	for _, c := range []*exec.Cmd{own, orphan, gotenberg} {
		waitState(t, c.Process.Pid, "Z")
	}

	if n := reapOnce(gotenberg.Process.Pid); n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
	if state(orphan.Process.Pid) != "" {
		t.Fatal("foreign zombie not reaped")
	}
	for _, c := range []*exec.Cmd{own, gotenberg} {
		if err := c.Wait(); err != nil {
			t.Fatalf("pid %d: Wait: %v (status stolen)", c.Process.Pid, err)
		}
	}
}
