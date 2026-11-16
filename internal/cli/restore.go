package cli

// berth restore-panel <file>: put a panel snapshot (from Settings → Panel snapshot) onto
// this machine. Run as root after installing Berth. The current /etc/berth and
// /var/lib/berth are kept beside it as *.before-restore-<time>.

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/wangsirco/berth/internal/snapshot"
)

var (
	restoreDataRe = regexp.MustCompile(`^/var/lib/berth(-[a-z0-9]+)?$`)
	restoreEtcRe  = regexp.MustCompile(`^/etc/berth(-[a-z0-9]+)?$`)
	restoreUnitRe = regexp.MustCompile(`^berth-[a-z0-9-]{1,40}$`)
)

// restoreOpts: where the snapshot goes. A second panel on the same machine (/etc/berth-dev,
// /var/lib/berth-dev) restores into its own folders and restarts only its own service.
type restoreOpts struct {
	check          bool
	etc, data      string
	units          []string
	passFile, file string
	rerun          map[string]rerunSpec // transient units, started again with systemd-run
}

type rerunSpec struct {
	Unit, User, Group, Mem string
	Argv                   []string
}

func parseRestoreArgs(args []string) (restoreOpts, error) {
	o := restoreOpts{etc: "/etc/berth", data: "/var/lib/berth", units: []string{"berth-panel", "berth-agent"}}
	usage := errors.New("usage: berth restore-panel [--check] [--etc DIR] [--data DIR] [--units a,b] [--password-file F] <file.berthsnap>")
	for i := 0; i < len(args); i++ {
		a := args[i]
		val := func() (string, error) {
			if i+1 >= len(args) {
				return "", usage
			}
			i++
			return args[i], nil
		}
		var err error
		switch a {
		case "--check":
			o.check = true
		case "--etc":
			o.etc, err = val()
		case "--data":
			o.data, err = val()
		case "--units":
			var v string
			v, err = val()
			o.units = strings.Split(v, ",")
		case "--password-file":
			o.passFile, err = val()
		case "--rerun":
			var v string
			if v, err = val(); err == nil {
				var sp rerunSpec
				b, derr := base64.StdEncoding.DecodeString(v)
				if derr != nil || json.Unmarshal(b, &sp) != nil || len(sp.Argv) == 0 || !restoreUnitRe.MatchString(sp.Unit) {
					return o, errors.New("bad --rerun")
				}
				if o.rerun == nil {
					o.rerun = map[string]rerunSpec{}
				}
				o.rerun[sp.Unit] = sp
			}
		default:
			if strings.HasPrefix(a, "--") || o.file != "" {
				return o, usage
			}
			o.file = a
		}
		if err != nil {
			return o, err
		}
	}
	if o.file == "" {
		return o, usage
	}
	if !restoreEtcRe.MatchString(o.etc) || !restoreDataRe.MatchString(o.data) {
		return o, errors.New("--etc must be /etc/berth[-name] and --data /var/lib/berth[-name]")
	}
	for _, u := range o.units {
		if !restoreUnitRe.MatchString(u) {
			return o, fmt.Errorf("bad unit name %q", u)
		}
	}
	return o, nil
}

// target: where a snapshot entry (always stored under etc/berth and var/lib/berth) goes.
func (o restoreOpts) target(name string) string {
	n := filepath.Clean(name)
	if rest, ok := strings.CutPrefix(n, "etc/berth"); ok {
		return o.etc + rest
	}
	rest, _ := strings.CutPrefix(n, "var/lib/berth")
	return o.data + rest
}

func cmdRestorePanel(args []string) error {
	o, err := parseRestoreArgs(args)
	if err != nil {
		return err
	}
	check := o.check
	if !check && os.Geteuid() != 0 {
		return errors.New("run it as root")
	}
	f, err := os.Open(o.file)
	if err != nil {
		return err
	}
	defer f.Close()
	pass := os.Getenv("BERTH_SNAPSHOT_PASSWORD")
	if o.passFile != "" {
		b, err := os.ReadFile(o.passFile)
		os.Remove(o.passFile) // read once: the password does not stay on disk
		if err != nil {
			return err
		}
		pass = strings.TrimRight(string(b), "\r\n")
	}
	if pass == "" {
		fmt.Fprint(os.Stderr, "Snapshot password: ")
		exec.Command("stty", "-echo").Run()
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		exec.Command("stty", "echo").Run()
		fmt.Fprintln(os.Stderr)
		pass = strings.TrimRight(line, "\r\n")
	}
	var plain bytes.Buffer
	if err := snapshot.Open(&plain, f, pass); err != nil {
		return err
	}
	// check every entry before touching anything
	gz, err := gzip.NewReader(bytes.NewReader(plain.Bytes()))
	if err != nil {
		return errors.New("the snapshot is damaged")
	}
	tr := tar.NewReader(gz)
	files, size, hasDB := 0, int64(0), false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("the snapshot is damaged")
		}
		n := filepath.Clean(h.Name)
		if !(n == "etc/berth" || strings.HasPrefix(n, "etc/berth/") || n == "var/lib/berth" || strings.HasPrefix(n, "var/lib/berth/")) || strings.Contains(n, "..") {
			return fmt.Errorf("unexpected entry %s", h.Name)
		}
		if h.Typeflag == tar.TypeReg {
			files++
			size += h.Size
			hasDB = hasDB || n == "var/lib/berth/berth.db"
		}
	}
	if !hasDB {
		return errors.New("the snapshot has no panel database")
	}
	fmt.Printf("snapshot ok: %d files, %d KB, panel database included\n", files, size/1024)
	if check {
		return nil
	}
	fmt.Println("stopping", strings.Join(o.units, " "))
	exec.Command("systemctl", append([]string{"stop"}, o.units...)...).Run()
	stamp := time.Now().Format("20060102-150405")
	// the snapshot holds what is inside the two folders, not the folders themselves: make them
	// again with the owner and mode the old ones had (the panel runs as berth and must get in)
	type top struct {
		mode     os.FileMode
		uid, gid int
	}
	tops := map[string]top{o.etc: {0o750, 0, berthGID()}, o.data: {0o750, berthUID(), berthGID()}}
	for d := range tops {
		if fi, err := os.Stat(d); err == nil {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				tops[d] = top{fi.Mode().Perm(), int(st.Uid), int(st.Gid)}
			}
		}
	}
	defer func() {
		for d, t := range tops {
			os.MkdirAll(d, t.mode)
			os.Chmod(d, t.mode)
			os.Chown(d, t.uid, t.gid)
		}
	}()
	for _, d := range []string{o.etc, o.data} {
		if _, err := os.Stat(d); err == nil {
			if err := os.Rename(d, d+".before-restore-"+stamp); err != nil {
				return err
			}
			fmt.Printf("kept the old %s as %s.before-restore-%s\n", d, d, stamp)
		}
	}
	gz, _ = gzip.NewReader(bytes.NewReader(plain.Bytes()))
	tr = tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		dst := o.target(h.Name)
		switch h.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(dst, os.FileMode(h.Mode)&0o7777)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(dst), 0o750)
			out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o7777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
		// owners by name: the berth user's id may differ on this machine
		uid, gid := h.Uid, h.Gid
		if u, err := user.Lookup(h.Uname); err == nil && h.Uname != "" {
			fmt.Sscan(u.Uid, &uid)
		}
		if g, err := user.LookupGroup(h.Gname); err == nil && h.Gname != "" {
			fmt.Sscan(g.Gid, &gid)
		}
		os.Lchown(dst, uid, gid)
	}
	for d, t := range tops { // before starting anything
		os.Chmod(d, t.mode)
		os.Chown(d, t.uid, t.gid)
	}
	exec.Command("restorecon", "-R", o.etc, o.data).Run()
	fmt.Println("starting", strings.Join(o.units, " "))
	for _, u := range o.units {
		var cmd *exec.Cmd
		if sp, ok := o.rerun[u]; ok {
			exec.Command("systemctl", "reset-failed", u).Run()
			a := []string{"--unit", sp.Unit}
			if sp.User != "" {
				a = append(a, "--uid", sp.User)
			}
			if sp.Group != "" {
				a = append(a, "--gid", sp.Group)
			}
			if sp.Mem != "" && sp.Mem != "infinity" {
				a = append(a, "-p", "MemoryMax="+sp.Mem)
			}
			cmd = exec.Command("systemd-run", append(a, sp.Argv...)...)
		} else {
			cmd = exec.Command("systemctl", "start", u)
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			return errors.New(strings.TrimSpace(string(out)))
		}
	}
	fmt.Println("restored. Remote hosts reconnect by themselves if this machine keeps the panel's address; otherwise run their join command again.")
	return nil
}

func berthUID() int {
	if u, err := user.Lookup("berth"); err == nil {
		var n int
		fmt.Sscan(u.Uid, &n)
		return n
	}
	return 0
}

func berthGID() int {
	if g, err := user.LookupGroup("berth"); err == nil {
		var n int
		fmt.Sscan(g.Gid, &n)
		return n
	}
	return 0
}
