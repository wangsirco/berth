package docker

// Reset a containerised database's admin password when the old one is lost (忘记 root 密码).
// PostgreSQL and MariaDB let the container's own root in over the local socket, so the
// password changes without a restart. MySQL does not: it is stopped, a throwaway copy
// starts on the same data with --skip-grant-tables and no network, the password is set
// there, and the original starts again.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/errs"
)

var (
	resetUserRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,63}$`)
	resetPassRe = regexp.MustCompile(`^[A-Za-z0-9!@#%^&*()_+=.,:;~-]{8,64}$`) // nothing that needs quoting
)

type rootReset struct {
	Container string `json:"container"`
	Engine    string `json:"engine"`
	User      string `json:"user"`
	Password  string `json:"password"`
}

func (c *Client) execOK(ctx context.Context, id string, cmd ...string) (string, bool) {
	out, code, err := c.Exec(ctx, id, cmd)
	return strings.TrimSpace(out), err == nil && code == 0
}

// mysqlSet changes the password for every host the account exists on. MariaDB uses SET
// PASSWORD, which keeps root's unix_socket login alongside the password.
func (c *Client) mysqlSet(ctx context.Context, id, cli, user, pw string, mariadb bool, skipGrants bool) (string, bool) {
	pre := ""
	if skipGrants {
		pre = "FLUSH PRIVILEGES; "
	}
	// (under --skip-grant-tables the FLUSH re-enables logins, so it goes in the last session only)
	hosts, ok := c.execOK(ctx, id, cli, "-uroot", "-N", "-B", "-e", "SELECT Host FROM mysql.user WHERE User='"+user+"'")
	if !ok {
		return hosts, false
	}
	var stmts []string
	for _, h := range strings.Fields(hosts) {
		if mariadb {
			stmts = append(stmts, fmt.Sprintf("SET PASSWORD FOR '%s'@'%s' = PASSWORD('%s')", user, h, pw))
		} else {
			stmts = append(stmts, fmt.Sprintf("ALTER USER '%s'@'%s' IDENTIFIED BY '%s'", user, h, pw))
		}
	}
	if len(stmts) == 0 {
		return "no account " + user, false
	}
	return c.execOK(ctx, id, cli, "-uroot", "-e", pre+strings.Join(stmts, "; ")+"; FLUSH PRIVILEGES;")
}

func (c *Client) ResetRootPassword(ctx context.Context, in rootReset, log func(string)) error {
	if !resetUserRe.MatchString(in.User) || !resetPassRe.MatchString(in.Password) {
		return errs.New(400, "db.reset_bad_password")
	}
	d, err := c.Inspect(ctx, in.Container)
	if err != nil {
		return err
	}
	if d.State != "running" {
		return errs.New(409, "db.reset_not_running", "container", in.Container)
	}
	switch in.Engine {
	case "postgres":
		log("psql over the container's local socket: ALTER USER " + in.User + " PASSWORD …")
		out, ok := c.execOK(ctx, in.Container, "psql", "-U", in.User, "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-c",
			fmt.Sprintf(`ALTER USER "%s" PASSWORD '%s'`, in.User, in.Password))
		if !ok {
			// the account may not be the bootstrap superuser: try as postgres
			out, ok = c.execOK(ctx, in.Container, "psql", "-U", "postgres", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-c",
				fmt.Sprintf(`ALTER USER "%s" PASSWORD '%s'`, in.User, in.Password))
		}
		if !ok {
			return errs.New(500, "db.reset_failed", "detail", out)
		}
		return nil
	case "mysql", "mariadb":
	default:
		return errs.New(400, "db.reset_unsupported", "engine", in.Engine)
	}
	// MariaDB images let root in over the socket (unix_socket); try that first
	mariadb := in.Engine == "mariadb"
	for _, cli := range []string{"mariadb", "mysql"} {
		if _, ok := c.execOK(ctx, in.Container, cli, "-uroot", "-e", "SELECT 1"); ok {
			log(cli + " -uroot over the container's local socket")
			out, ok := c.mysqlSet(ctx, in.Container, cli, in.User, in.Password, mariadb, false)
			if !ok {
				return errs.New(500, "db.reset_failed", "detail", out)
			}
			return nil
		}
	}
	// MySQL: a throwaway copy on the same data, with no grant checks and no network
	tmp := "berth-dbreset-" + strings.TrimPrefix(d.Name, "/")
	docker := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	docker("rm", "-f", tmp)
	log("docker stop " + in.Container)
	if out, err := docker("stop", "-t", "60", in.Container); err != nil {
		return errs.New(500, "db.reset_failed", "detail", out)
	}
	defer func() {
		docker("rm", "-f", tmp)
		log("docker start " + in.Container)
		docker("start", in.Container)
	}()
	log("docker run " + tmp + " (" + d.Image + " --skip-grant-tables --skip-networking)")
	if out, err := docker("run", "-d", "--name", tmp, "--volumes-from", in.Container, "--network", "none", d.Image, "--skip-grant-tables", "--skip-networking"); err != nil {
		return errs.New(500, "db.reset_failed", "detail", out)
	}
	up, cli := false, "mysql"
	for i := 0; i < 60 && !up; i++ {
		time.Sleep(time.Second)
		for _, adm := range []string{"mysqladmin", "mariadb-admin"} {
			if _, ok := c.execOK(ctx, tmp, adm, "ping", "--silent"); ok {
				up = true
				if adm == "mariadb-admin" {
					cli = "mariadb"
				}
				break
			}
		}
	}
	if !up {
		logs, _ := docker("logs", "--tail", "5", tmp)
		return errs.New(500, "db.reset_failed", "detail", "the temporary copy did not start: "+logs)
	}
	out, ok := c.mysqlSet(ctx, tmp, cli, in.User, in.Password, mariadb, true)
	if !ok {
		return errs.New(500, "db.reset_failed", "detail", out)
	}
	docker("stop", "-t", "60", tmp)
	return nil
}

func registerDBReset(s *agent.Server, c *Client) {
	s.Handle("POST /v1/db/root-reset", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in rootReset
		json.Unmarshal(body, &in)
		st := agent.NewStream(w)
		st.End(c.ResetRootPassword(r.Context(), in, func(l string) { st.Send(map[string]string{"l": l}) }))
	})
}
