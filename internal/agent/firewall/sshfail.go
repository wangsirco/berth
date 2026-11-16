package firewall

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// AuthFailure is one failed sign-in attempt seen in a log.
type AuthFailure struct {
	TS    int64  `json:"ts"` // unix seconds
	IP    string `json:"ip"`
	User  string `json:"user,omitempty"`
	Kind  string `json:"kind"`  // ssh
	Count int    `json:"count"` // attempts in this session
}

var (
	sshFailed  = regexp.MustCompile(`^Failed (?:password|publickey|keyboard-interactive/pam) for (?:invalid user )?(\S*) from (\S+) port`)
	sshInvalid = regexp.MustCompile(`^Invalid user (\S*) from (\S+) port`)
	sshClosed  = regexp.MustCompile(`^(?:Connection closed|Disconnected) by (?:authenticating|invalid) user (\S*) (\S+) port \d+ \[preauth\]`)
	sshMaxAuth = regexp.MustCompile(`^error: maximum authentication attempts exceeded for (?:invalid user )?(\S*) from (\S+) port`)
	// OpenSSH 9.8+ refuses repeat offenders itself (PerSourcePenalties) and logs each refusal
	sshPenalty = regexp.MustCompile(`^drop connection #\d+ from \[([^\]]+)\]:\d+ on .* penalty: (?:attempted authentication by invalid user|failed authentication|exceeded LoginGraceTime)`)
)

type journalEntry struct {
	Message  string `json:"MESSAGE"`
	PID      string `json:"_PID"`
	Realtime string `json:"__REALTIME_TIMESTAMP"`
	Cursor   string `json:"__CURSOR"`
}

// ParseSSH turns sshd journal entries into failures, one per session (sshd logs several
// lines for one attempt: "Invalid user" and then "Failed password for invalid user").
func ParseSSH(entries []journalEntry) []AuthFailure {
	type sess struct {
		f      AuthFailure
		failed int
		other  bool
	}
	by := map[string]*sess{}
	var order []string
	for _, e := range entries {
		var m []string
		kind := ""
		for _, re := range []struct {
			re   *regexp.Regexp
			kind string
		}{{sshFailed, "failed"}, {sshInvalid, "other"}, {sshClosed, "other"}, {sshMaxAuth, "other"}} {
			if m = re.re.FindStringSubmatch(e.Message); m != nil {
				kind = re.kind
				break
			}
		}
		if m == nil {
			if p := sshPenalty.FindStringSubmatch(e.Message); p != nil {
				m, kind = []string{p[0], "", p[1]}, "other"
				e.PID += "/drop" + e.Cursor + e.Realtime // each refused connection is its own attempt
			}
		}
		if m == nil || net.ParseIP(m[2]) == nil {
			continue
		}
		key := e.PID + "/" + m[2]
		s := by[key]
		if s == nil {
			us, _ := strconv.ParseInt(e.Realtime, 10, 64)
			s = &sess{f: AuthFailure{TS: us / 1e6, IP: m[2], User: m[1], Kind: "ssh"}}
			by[key] = s
			order = append(order, key)
		}
		if kind == "failed" {
			s.failed++
		} else {
			s.other = true
		}
	}
	var out []AuthFailure
	for _, k := range order {
		s := by[k]
		s.f.Count = s.failed
		if s.f.Count == 0 && s.other {
			s.f.Count = 1
		}
		out = append(out, s.f)
	}
	return out
}

// SSHFailures reads sshd's journal after cursor (the last 15 minutes when cursor is empty)
// and returns the failures and the cursor to continue from.
func SSHFailures(ctx context.Context, cursor string) ([]AuthFailure, string, error) {
	args := []string{"-u", "sshd.service", "-o", "json", "--no-pager", "-q"}
	if cursor != "" {
		args = append(args, "--after-cursor", cursor)
	} else {
		args = append(args, "--since", "-15min")
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, cursor, err
	}
	if err := cmd.Start(); err != nil {
		return nil, cursor, err
	}
	var entries []journalEntry
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e journalEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			// MESSAGE may be a byte array for non-UTF-8 text; skip such lines
			continue
		}
		e.Message = strings.TrimSpace(e.Message)
		entries = append(entries, e)
		cursor = e.Cursor
		if len(entries) > 20000 { // a flood: the rest next time
			break
		}
	}
	cmd.Process.Kill()
	cmd.Wait()
	return ParseSSH(entries), cursor, nil
}

// EnsureTable makes sure `inet berth` exists (the confirmed rules, or an empty table with
// the ban sets) so bans work before anyone adds a firewall rule.
func EnsureTable(ctx context.Context) error {
	if _, err := run(ctx, "nft", "list", "table", "inet", "berth"); err == nil {
		return nil
	}
	Boot(ctx)
	if _, err := run(ctx, "nft", "list", "table", "inet", "berth"); err == nil {
		return nil
	}
	if out, err := runStdin(ctx, Render(nil, nil), "nft", "-f", "-"); err != nil {
		return &nftError{out}
	}
	return nil
}

type nftError struct{ out string }

func (e *nftError) Error() string { return "nft: " + e.out }
