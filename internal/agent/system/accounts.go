package system

// System users and groups (方案 §5.14): list, create, delete, lock, groups, password.
// Accounts below UID 1000 belong to the system and are only listed.

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Account struct {
	Name    string   `json:"name"`
	UID     int      `json:"uid"`
	GID     int      `json:"gid"`
	Gecos   string   `json:"gecos,omitempty"`
	Home    string   `json:"home"`
	Shell   string   `json:"shell"`
	Groups  []string `json:"groups"`
	System  bool     `json:"system"`
	NoLogin bool     `json:"nologin"`
	Locked  bool     `json:"locked"`
	NoPass  bool     `json:"no_password"` // no password set (key login only)
}

type Group struct {
	Name    string   `json:"name"`
	GID     int      `json:"gid"`
	Members []string `json:"members"`
	System  bool     `json:"system"`
}

type AccountList struct {
	Users      []Account `json:"users"`
	Groups     []Group   `json:"groups"`
	AdminGroup string    `json:"admin_group"` // wheel or sudo
	Shells     []string  `json:"shells"`
}

func readColon(path string, n int) [][]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out [][]string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		p := strings.Split(l, ":")
		if len(p) >= n {
			out = append(out, p)
		}
	}
	return out
}

func Accounts() AccountList {
	out := AccountList{Users: []Account{}, Groups: []Group{}, Shells: []string{}}
	members := map[string][]string{}
	gname := map[int]string{}
	for _, g := range readColon("/etc/group", 4) {
		gid, _ := strconv.Atoi(g[2])
		ms := []string{}
		if g[3] != "" {
			ms = strings.Split(g[3], ",")
		}
		out.Groups = append(out.Groups, Group{Name: g[0], GID: gid, Members: ms, System: gid < 1000 || gid >= 60000})
		gname[gid] = g[0]
		for _, m := range ms {
			members[m] = append(members[m], g[0])
		}
		if g[0] == "wheel" || g[0] == "sudo" && out.AdminGroup == "" {
			out.AdminGroup = g[0]
		}
	}
	shadow := map[string]string{}
	expired := map[string]bool{} // locked accounts are expired too, which also stops key login
	today := int(time.Now().Unix() / 86400)
	for _, s := range readColon("/etc/shadow", 2) {
		shadow[s[0]] = s[1]
		if len(s) > 7 && s[7] != "" {
			if d, err := strconv.Atoi(s[7]); err == nil && d <= today {
				expired[s[0]] = true
			}
		}
	}
	for _, p := range readColon("/etc/passwd", 7) {
		uid, _ := strconv.Atoi(p[2])
		gid, _ := strconv.Atoi(p[3])
		a := Account{Name: p[0], UID: uid, GID: gid, Gecos: p[4], Home: p[5], Shell: p[6], System: uid < 1000 || uid >= 60000,
			NoLogin: strings.HasSuffix(p[6], "nologin") || strings.HasSuffix(p[6], "false")}
		a.Groups = append([]string{}, members[p[0]]...)
		if pg := gname[gid]; pg != "" && pg != p[0] {
			a.Groups = append([]string{pg}, a.Groups...)
		}
		h := shadow[p[0]]
		a.Locked = expired[p[0]]
		a.NoPass = h == "" || h == "!" || h == "!!" || h == "*" || h == "!*"
		out.Users = append(out.Users, a)
	}
	sort.SliceStable(out.Users, func(i, j int) bool {
		if out.Users[i].System != out.Users[j].System {
			return !out.Users[i].System
		}
		return out.Users[i].UID < out.Users[j].UID
	})
	if b, err := os.ReadFile("/etc/shells"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); strings.HasPrefix(l, "/") {
				out.Shells = append(out.Shells, l)
			}
		}
	}
	return out
}

func findAccount(name string) (Account, bool) {
	for _, u := range Accounts().Users {
		if u.Name == name {
			return u, true
		}
	}
	return Account{}, false
}

// AccountOp is one change to users or groups.
type AccountOp struct {
	Op         string   `json:"op"` // create | delete | lock | unlock | groups | password | shell | group_create | group_delete
	Name       string   `json:"name"`
	Shell      string   `json:"shell,omitempty"`
	Groups     []string `json:"groups,omitempty"`
	Password   string   `json:"password,omitempty"`
	RemoveHome bool     `json:"remove_home,omitempty"`
	Gecos      string   `json:"gecos,omitempty"`
}

func setPassword(ctx context.Context, name, pw string) error {
	if strings.ContainsAny(pw, ":\n\r") {
		return errors.New("the password may not contain ':' or line breaks")
	}
	cmd := exec.CommandContext(ctx, "chpasswd")
	cmd.Stdin = strings.NewReader(name + ":" + pw + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}

func validGroups(gs []string) error {
	for _, g := range gs {
		if !unameRe.MatchString(g) {
			return errors.New("bad group name: " + g)
		}
	}
	return nil
}

func ApplyAccount(ctx context.Context, op AccountOp) error {
	if !unameRe.MatchString(op.Name) {
		return errors.New("bad name (lowercase letters, digits, - and _, starting with a letter)")
	}
	if err := validGroups(op.Groups); err != nil {
		return err
	}
	if strings.ContainsAny(op.Gecos, ":\n") {
		return errors.New("bad display name")
	}
	if op.Shell != "" {
		ok := strings.HasSuffix(op.Shell, "/nologin")
		for _, sh := range Accounts().Shells {
			ok = ok || sh == op.Shell
		}
		if !ok {
			return errors.New("not a login shell listed in /etc/shells: " + op.Shell)
		}
	}
	exists := func() (Account, error) {
		a, ok := findAccount(op.Name)
		if !ok {
			return a, errors.New("no such user: " + op.Name)
		}
		if a.System || a.Name == "root" {
			return a, errors.New("refuse:account_system")
		}
		return a, nil
	}
	do := func(name string, args ...string) error {
		if out, err := run(ctx, name, args...); err != nil {
			return errors.New(out)
		}
		return nil
	}
	switch op.Op {
	case "create":
		if _, ok := findAccount(op.Name); ok {
			return errors.New("refuse:account_exists")
		}
		args := []string{"-m"}
		if op.Shell == "" {
			op.Shell = "/bin/bash"
		}
		args = append(args, "-s", op.Shell)
		if op.Gecos != "" {
			args = append(args, "-c", op.Gecos)
		}
		if len(op.Groups) > 0 {
			args = append(args, "-G", strings.Join(op.Groups, ","))
		}
		if err := do("useradd", append(args, op.Name)...); err != nil {
			return err
		}
		if op.Password != "" {
			return setPassword(ctx, op.Name, op.Password)
		}
		return nil
	case "delete":
		if _, err := exists(); err != nil {
			return err
		}
		// a user's own systemd manager lingers after logging out: end it, but refuse when
		// anything else of theirs still runs
		if out, _ := run(ctx, "pgrep", "-u", op.Name, "-l"); out != "" {
			for _, l := range strings.Split(out, "\n") {
				if f := strings.Fields(l); len(f) == 2 && f[1] != "systemd" && f[1] != "(sd-pam)" {
					return errors.New("refuse:account_busy")
				}
			}
			run(ctx, "loginctl", "terminate-user", op.Name)
			for i := 0; i < 20; i++ {
				if out, _ := run(ctx, "pgrep", "-u", op.Name); out == "" {
					break
				}
				time.Sleep(250 * time.Millisecond)
			}
		}
		args := []string{}
		if op.RemoveHome {
			args = append(args, "-r")
		}
		return do("userdel", append(args, op.Name)...)
	case "lock":
		if _, err := exists(); err != nil {
			return err
		}
		// -L blocks the password, -e 1 (expired) blocks every login including keys
		return do("usermod", "-L", "-e", "1", op.Name)
	case "unlock":
		a, err := exists()
		if err != nil {
			return err
		}
		if err := do("usermod", "-e", "", op.Name); err != nil {
			return err
		}
		if !a.NoPass {
			return do("usermod", "-U", op.Name)
		}
		return nil
	case "groups":
		a, err := exists()
		if err != nil {
			return err
		}
		// keep the primary group out of the supplementary list
		var gs []string
		for _, g := range op.Groups {
			if g != a.Name {
				gs = append(gs, g)
			}
		}
		return do("usermod", "-G", strings.Join(gs, ","), op.Name)
	case "shell":
		if _, err := exists(); err != nil {
			return err
		}
		return do("usermod", "-s", op.Shell, op.Name)
	case "password":
		if _, err := exists(); err != nil {
			return err
		}
		if op.Password == "" {
			return do("passwd", "-d", op.Name)
		}
		return setPassword(ctx, op.Name, op.Password)
	case "group_create":
		return do("groupadd", op.Name)
	case "group_delete":
		for _, g := range Accounts().Groups {
			if g.Name == op.Name && g.System {
				return errors.New("refuse:account_system")
			}
		}
		return do("groupdel", op.Name)
	}
	return errors.New("unknown operation")
}
