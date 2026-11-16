package system

// Native Redis (R4 follow-up): Valkey on Fedora (the Redis fork the distribution ships, same
// protocol and commands), redis-server on Debian. Berth's settings go in one file the main
// config includes last: listening, the password, memory.

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func redisDir() string {
	if debianLike() {
		return "/etc/redis"
	}
	return "/etc/valkey"
}

func redisConfPath() string { return filepath.Join(redisDir(), "berth.conf") }

func redisCLI() string {
	if debianLike() {
		return "redis-cli"
	}
	return "valkey-cli"
}

// redisConfText is Berth's file: reachable from containers (the berth_db table keeps it in),
// a password always, and the tuning.
func redisConfText(d DBNative) string {
	var b strings.Builder
	b.WriteString("# Managed by Berth (Databases). Changes here are overwritten.\n")
	b.WriteString("bind 0.0.0.0 -::\nprotected-mode no\n")
	fmt.Fprintf(&b, "requirepass %s\n", d.Pass)
	for _, k := range sortedKeys(d.Values) {
		fmt.Fprintf(&b, "%s %s\n", k, d.Values[k])
	}
	return b.String()
}

func writeRedisConfig(d DBNative) error {
	main := filepath.Join(redisDir(), filepath.Base(redisDir())+".conf") // valkey.conf / redis.conf
	b, err := os.ReadFile(main)
	if err != nil {
		return err
	}
	inc := "include " + redisConfPath()
	if !strings.Contains(string(b), inc) {
		b = append(b, []byte("\n# added by Berth: its settings come last\n"+inc+"\n")...)
		if err := os.WriteFile(main, b, 0o640); err != nil {
			return err
		}
	}
	if err := os.WriteFile(redisConfPath(), []byte(redisConfText(d)), 0o640); err != nil {
		return err
	}
	grp := "valkey"
	if debianLike() {
		grp = "redis"
	}
	exec.Command("chown", "root:"+grp, redisConfPath(), main).Run()
	if !debianLike() {
		exec.Command("restorecon", redisConfPath()).Run()
	}
	return nil
}

// redisUp: the server answers on its port (PONG, or "auth required" — both mean it runs).
func redisUp() bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:6379", 2*time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprint(c, "PING\r\n")
	l, _ := bufio.NewReader(c).ReadString('\n')
	return strings.HasPrefix(l, "+PONG") || strings.HasPrefix(l, "-NOAUTH")
}

// redisCmd runs the CLI with the password in its environment (never on the command line).
func redisCmd(ctx context.Context, pass string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, redisCLI(), append([]string{"--no-auth-warning", "-h", "127.0.0.1"}, args...)...)
	cmd.Env = append(os.Environ(), "REDISCLI_AUTH="+pass)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

var (
	redisTunable = map[string]bool{"maxmemory": true, "maxmemory-policy": true, "appendonly": true, "maxclients": true}
	redisValRe   = regexp.MustCompile(`^([0-9]+(kb|mb|gb)?|yes|no|noeviction|allkeys-lru|allkeys-lfu|volatile-lru|volatile-lfu|allkeys-random|volatile-random|volatile-ttl)$`)
)

func redisLive(ctx context.Context, d DBNative) map[string]string {
	out := map[string]string{}
	for k := range redisTunable {
		if v, err := redisCmd(ctx, d.Pass, "CONFIG", "GET", k); err == nil {
			if l := strings.Split(v, "\n"); len(l) == 2 {
				out[k] = strings.TrimSpace(l[1])
			}
		}
	}
	return out
}
