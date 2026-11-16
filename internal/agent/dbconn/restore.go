package dbconn

// Import: load an .sql or .sql.gz file into one database by running the engine's client
// inside the database container (or on this host for a native database) (宝塔/1Panel 的“导入”).

import (
	"compress/gzip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wangsirco/berth/internal/errs"
)

// With replace, the database is emptied first (used to put a saved copy back exactly).
func Import(ctx context.Context, container string, t Target, path string, replace bool) error {
	if !identRe.MatchString(t.Database) {
		return errs.New(400, "db.bad_name")
	}
	path = filepath.Clean(path)
	if !strings.HasSuffix(path, ".sql") && !strings.HasSuffix(path, ".sql.gz") {
		return errs.New(400, "db.bad_import_file")
	}
	f, err := os.Open(path)
	if err != nil {
		return errs.New(404, "db.import_missing", "path", path)
	}
	defer f.Close()
	var in io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return errs.New(400, "db.bad_import_file")
		}
		defer gz.Close()
		in = gz
	}
	if replace {
		var pre *exec.Cmd
		switch t.Engine {
		case "postgres":
			pre = toolCmd(ctx, container, t, "client", "-v", "ON_ERROR_STOP=1", "-q", "-U", t.User, "-d", t.Database, "-c", "DROP SCHEMA public CASCADE; CREATE SCHEMA public;")
		case "mysql", "mariadb":
			pre = toolCmd(ctx, container, t, "client", "-u", t.User, "-e",
				"DROP DATABASE `"+t.Database+"`; CREATE DATABASE `"+t.Database+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;")
		default:
			return errs.New(400, "db.bad_engine", "engine", t.Engine)
		}
		if out, err := pre.CombinedOutput(); err != nil {
			return errs.New(500, "db.import_failed", "detail", strings.TrimSpace(string(out)))
		}
	}
	var c *exec.Cmd
	switch t.Engine {
	case "postgres":
		c = toolCmd(ctx, container, t, "client", "-v", "ON_ERROR_STOP=1", "-q", "-U", t.User, "-d", t.Database)
	case "mysql", "mariadb":
		c = toolCmd(ctx, container, t, "client", "-u", t.User, t.Database)
	default:
		return errs.New(400, "db.bad_engine", "engine", t.Engine)
	}
	c.Stdin = in
	var stderr strings.Builder
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return errs.New(500, "db.import_failed", "detail", strings.TrimSpace(stderr.String()+" "+err.Error()))
	}
	return nil
}
