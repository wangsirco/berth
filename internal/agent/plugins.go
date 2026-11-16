package agent

// Plugins (the panel's 插件): unpacking a package the panel has checked, and running a native
// plugin as its own systemd service — under its own system user with a read-only system, or as
// root when the person installing it agreed to that.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/plugin"
)

var pluginPkgRe = regexp.MustCompile(`^/var/lib/berth(-[a-z0-9]+)?/plugins/[0-9a-f]{64}\.berthplugin$`)

// ContainerPluginDir: where a container plugin's package is unpacked (its build folder lives there).
func ContainerPluginDir(id string) string { return "/srv/berth/plugins/" + id + "/src" }

func cmdOut(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// unpackPlugin writes the package's files into dir (replacing what was there).
func unpackPlugin(file, dir string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tmp := dir + ".new"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if h.Typeflag != tar.TypeReg || name == "." || strings.HasPrefix(name, "..") || strings.HasPrefix(name, "/") || name == plugin.SigName || name == plugin.SignerName {
			continue
		}
		dst := filepath.Join(tmp, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(dst), 0o755)
		mode := os.FileMode(0o644)
		if h.Mode&0o111 != 0 {
			mode = 0o755
		}
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		f.Close()
	}
	os.RemoveAll(dir)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, dir)
}

func registerPlugins(s *Server) {
	s.Handle("POST /v1/plugins/unpack", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			File string `json:"file"`
			ID   string `json:"id"`
			Kind string `json:"kind"` // native | container
		}
		json.Unmarshal(body, &in)
		if !pluginPkgRe.MatchString(in.File) || !plugin.IDRe.MatchString(in.ID) {
			WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		dir := ContainerPluginDir(in.ID)
		if in.Kind == "native" {
			dir = path.Join(plugin.NativeRoot, in.ID, "app")
		}
		if err := unpackPlugin(in.File, dir); err != nil {
			WriteErr(w, errs.New(500, "plugin.unpack_failed", "detail", err.Error()))
			return
		}
		WriteJSON(w, 200, map[string]string{"dir": dir})
	})

	s.Handle("POST /v1/plugins/native/install", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			ID    string `json:"id"`
			Unit  string `json:"unit"`
			Env   string `json:"env"`
			Root  bool   `json:"root"`
			Start bool   `json:"start"`
		}
		json.Unmarshal(body, &in)
		if !plugin.IDRe.MatchString(in.ID) || !strings.Contains(in.Unit, "ExecStart="+path.Join(plugin.NativeRoot, in.ID, "app")+"/") {
			WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		if err := installNative(r.Context(), in.ID, in.Unit, in.Env, in.Root); err != nil {
			WriteErr(w, errs.New(500, "plugin.install_failed", "detail", err.Error()))
			return
		}
		WriteJSON(w, 200, map[string]any{"unit": plugin.Unit(in.ID), "active": nativeActive(r.Context(), in.ID)})
	})

	s.Handle("POST /v1/plugins/native/remove", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			ID       string `json:"id"`
			KeepData bool   `json:"keep_data"`
		}
		json.Unmarshal(body, &in)
		if !plugin.IDRe.MatchString(in.ID) {
			WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		removeNative(r.Context(), in.ID, in.KeepData)
		WriteJSON(w, 200, map[string]bool{"ok": true})
	})

	s.Handle("POST /v1/plugins/native/status", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			IDs []string `json:"ids"`
		}
		json.Unmarshal(body, &in)
		out := map[string]bool{}
		for _, id := range in.IDs {
			if plugin.IDRe.MatchString(id) {
				out[id] = nativeActive(r.Context(), id)
			}
		}
		WriteJSON(w, 200, out)
	})
}

func nativeActive(ctx context.Context, id string) bool {
	out, _ := cmdOut(ctx, "systemctl", "is-active", plugin.Unit(id))
	return out == "active"
}

// installNative: a failed install takes back what it made (the data folder and the user stay
// only when they were there before, i.e. on an update).
func installNative(ctx context.Context, id, unit, env string, root bool) (err error) {
	data := path.Join(plugin.NativeRoot, id, "data")
	_, statErr := os.Stat(data)
	existed := statErr == nil
	defer func() {
		if err != nil {
			removeNative(context.Background(), id, existed)
		}
	}()
	if err := os.MkdirAll(data, 0o750); err != nil {
		return err
	}
	if !root {
		u := plugin.User(id)
		if _, err := cmdOut(ctx, "id", "-u", u); err != nil {
			if out, err := cmdOut(ctx, "useradd", "--system", "--home-dir", data, "--no-create-home", "--shell", "/sbin/nologin", "--comment", "Berth plugin "+id, u); err != nil {
				return errors.New("useradd: " + out)
			}
		}
		if out, err := cmdOut(ctx, "chown", "-R", u+":"+u, data); err != nil {
			return errors.New("chown: " + out)
		}
	}
	if err := os.MkdirAll(plugin.EnvDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path.Join(plugin.EnvDir, id+".env"), []byte(env), 0o600); err != nil {
		return err
	}
	unitPath := "/etc/systemd/system/" + plugin.Unit(id) + ".service"
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	labelPlugins(ctx)
	cmdOut(ctx, "restorecon", unitPath)
	if out, err := cmdOut(ctx, "systemctl", "daemon-reload"); err != nil {
		return errors.New(out)
	}
	if out, err := cmdOut(ctx, "systemctl", "enable", plugin.Unit(id)); err != nil {
		return errors.New(out)
	}
	if out, err := cmdOut(ctx, "systemctl", "restart", plugin.Unit(id)); err != nil {
		logs, _ := cmdOut(ctx, "journalctl", "-u", plugin.Unit(id), "-n", "20", "--no-pager")
		return errors.New(out + "\n" + logs)
	}
	time.Sleep(1500 * time.Millisecond)
	if !nativeActive(ctx, id) {
		logs, _ := cmdOut(ctx, "journalctl", "-u", plugin.Unit(id), "-n", "20", "--no-pager")
		return errors.New("it stopped right after starting:\n" + logs)
	}
	return nil
}

func removeNative(ctx context.Context, id string, keepData bool) {
	cmdOut(ctx, "systemctl", "disable", "--now", plugin.Unit(id))
	os.Remove("/etc/systemd/system/" + plugin.Unit(id) + ".service")
	cmdOut(ctx, "systemctl", "daemon-reload")
	os.Remove(path.Join(plugin.EnvDir, id+".env"))
	os.RemoveAll(path.Join(plugin.NativeRoot, id, "app"))
	if !keepData {
		os.RemoveAll(path.Join(plugin.NativeRoot, id))
		cmdOut(ctx, "userdel", plugin.User(id))
	}
}

// labelPlugins: under SELinux, files in /var/lib are not executable by services; mark the
// plugins' app folders as programs (bin_t), persistently when semanage is there.
func labelPlugins(ctx context.Context) {
	if out, _ := cmdOut(ctx, "getenforce"); out == "" || out == "Disabled" {
		return
	}
	spec := plugin.NativeRoot + "/[^/]+/app(/.*)?"
	if out, err := cmdOut(ctx, "semanage", "fcontext", "-a", "-t", "bin_t", spec); err == nil || strings.Contains(out, "already defined") {
		cmdOut(ctx, "restorecon", "-R", plugin.NativeRoot)
		return
	}
	ents, _ := os.ReadDir(plugin.NativeRoot)
	for _, e := range ents {
		if e.IsDir() {
			cmdOut(ctx, "chcon", "-R", "-t", "bin_t", path.Join(plugin.NativeRoot, e.Name(), "app"))
		}
	}
}
