package dbconn

// toolCmd runs one of the engine's command-line tools (dump, client) against a target: inside
// its container when there is one, otherwise on this host (native databases, R4). The password
// travels in the environment (MYSQL_PWD / PGPASSWORD), never on a command line.

import (
	"context"
	"net"
	"os"
	"os/exec"
)

func pwVar(engine string) string {
	if engine == "postgres" {
		return "PGPASSWORD"
	}
	return "MYSQL_PWD"
}

// tool names: "dump" or "client" per engine
var toolNames = map[string]map[string]string{
	"postgres": {"dump": "pg_dump", "client": "psql"},
	"mysql":    {"dump": "mysqldump", "client": "mysql"},
	"mariadb":  {"dump": "mariadb-dump", "client": "mariadb"},
}

func toolCmd(ctx context.Context, container string, t Target, tool string, args ...string) *exec.Cmd {
	name := toolNames[t.Engine][tool]
	pw := pwVar(t.Engine) + "=" + t.Password
	if container != "" {
		// "-e NAME" without a value: docker copies it from its own environment
		c := exec.CommandContext(ctx, "docker", append([]string{"exec", "-i", "-e", pwVar(t.Engine), container, name}, args...)...)
		c.Env = append(os.Environ(), pw)
		return c
	}
	host, port, _ := net.SplitHostPort(t.Addr)
	var conn []string
	if t.Engine == "postgres" {
		conn = []string{"-h", host, "-p", port}
	} else {
		conn = []string{"-h", host, "-P", port, "--protocol=TCP"}
	}
	c := exec.CommandContext(ctx, name, append(conn, args...)...)
	c.Env = append(os.Environ(), pw)
	return c
}
