package system

// Scheduled jobs (方案 §5.14). Berth's own jobs are kept and timed by the panel (so a job
// can also run a Berth action, and every run is recorded); the agent only executes the
// script. What the system already schedules — /etc/crontab, /etc/cron.d, per-user
// crontabs and systemd timers — is listed read-only, so nothing is hidden.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/wangsirco/berth/internal/sysexec"
)

type CronEntry struct {
	Source   string `json:"source"` // file it came from
	User     string `json:"user"`
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
}

type Timer struct {
	Unit      string `json:"unit"`
	Activates string `json:"activates"`
	Next      int64  `json:"next,omitempty"` // unix seconds
	Last      int64  `json:"last,omitempty"`
}

var envLine = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*=`)

// parseCrontab reads one crontab; system ones (/etc/crontab, /etc/cron.d) carry a user field.
func parseCrontab(r io.Reader, source, owner string, system bool) []CronEntry {
	var out []CronEntry
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") || envLine.MatchString(l) {
			continue
		}
		f := strings.Fields(l)
		n := 5
		if strings.HasPrefix(f[0], "@") {
			n = 1
		}
		if system {
			n++
		}
		if len(f) <= n {
			continue
		}
		e := CronEntry{Source: source, User: owner, Schedule: strings.Join(f[:n], " "), Command: strings.Join(f[n:], " ")}
		if system {
			e.User = f[n-1]
			e.Schedule = strings.Join(f[:n-1], " ")
		}
		out = append(out, e)
	}
	return out
}

func Crontabs() []CronEntry {
	out := []CronEntry{}
	read := func(path, owner string, system bool) {
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		out = append(out, parseCrontab(f, path, owner, system)...)
	}
	read("/etc/crontab", "", true)
	if ents, err := os.ReadDir("/etc/cron.d"); err == nil {
		for _, e := range ents {
			if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				read(filepath.Join("/etc/cron.d", e.Name()), "", true)
			}
		}
	}
	for _, dir := range []string{"/var/spool/cron", "/var/spool/cron/crontabs"} {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if !e.IsDir() {
				read(filepath.Join(dir, e.Name()), e.Name(), false)
			}
		}
	}
	return out
}

func Timers(ctx context.Context) []Timer {
	out := []Timer{}
	b, err := run(ctx, "systemctl", "list-timers", "--all", "--output=json", "--no-pager")
	if err != nil {
		return out
	}
	var raw []struct {
		Next      int64  `json:"next"` // microseconds
		Last      int64  `json:"last"`
		Unit      string `json:"unit"`
		Activates string `json:"activates"`
	}
	if json.Unmarshal([]byte(b), &raw) != nil {
		return out
	}
	for _, t := range raw {
		out = append(out, Timer{Unit: t.Unit, Activates: t.Activates, Next: t.Next / 1e6, Last: t.Last / 1e6})
	}
	return out
}

var unameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// ExecJob runs a job's script as `who` (default root) with a time limit, in a scope of its
// own, feeding each output line to line. It returns the exit code.
func ExecJob(ctx context.Context, script, who string, timeout time.Duration, line func(string)) (int, error) {
	if strings.TrimSpace(script) == "" {
		return -1, errors.New("empty script")
	}
	if who == "" {
		who = "root"
	}
	if !unameRe.MatchString(who) {
		return -1, errors.New("bad user")
	}
	u, err := user.Lookup(who)
	if err != nil {
		return -1, errors.New("no such user: " + who)
	}
	if timeout <= 0 || timeout > 24*time.Hour {
		timeout = time.Hour
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	name, args := sysexec.Scoped("runuser", "-u", who, "--", "bash", "-c", script)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = u.HomeDir
	if _, err := os.Stat(cmd.Dir); err != nil {
		cmd.Dir = "/"
	}
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=" + u.HomeDir, "USER=" + who, "LANG=C.UTF-8", "BERTH_JOB=1"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line(sc.Text())
		}
		io.Copy(io.Discard, pr)
		close(done)
	}()
	err = cmd.Wait()
	pw.Close()
	<-done
	if ctx.Err() == context.DeadlineExceeded {
		return -1, errors.New("timed out after " + timeout.String())
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}
