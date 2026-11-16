package agent

// Virus scan (病毒扫描, ClamAV): clamscan in a throwaway container, so nothing is installed on
// the host and its ~1 GB of signatures is only in memory while a scan runs. The host is
// mounted read-only; the signature database lives in a volume and is refreshed once a day.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sysexec"
)

const (
	avImage  = "clamav/clamav-debian:stable" // the plain clamav/clamav image has no arm64 build
	avVolume = "berth-clamav-db"
)

var avUpdated = DataDir + "/clamav-updated"

type AVHit struct {
	Path string `json:"path"`
	Sig  string `json:"sig"`
}

type AVResult struct {
	Paths    []string `json:"paths"`
	Files    int      `json:"files"`
	Infected []AVHit  `json:"infected"`
	Seconds  int      `json:"seconds"`
	DBDate   string   `json:"db_date,omitempty"`
}

var avDenied = []string{"/proc", "/sys", "/dev", "/run"}

func avCmd(ctx context.Context, _ func(string), args ...string) (string, error) {
	n, a := sysexec.Scoped("docker", args...)
	cmd := exec.CommandContext(ctx, n, a...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Scan scans the given paths (files or directories).
func Scan(ctx context.Context, paths []string, log func(string)) (AVResult, error) {
	res := AVResult{Paths: paths, Infected: []AVHit{}}
	if len(paths) == 0 || len(paths) > 20 {
		return res, errors.New("give 1–20 paths")
	}
	args := []string{"run", "--rm", "--name", "berth-clamav-" + strconv.FormatInt(time.Now().Unix(), 36), "--memory", "2g",
		"--security-opt", "label=disable", "--network", "none", "-v", avVolume + ":/var/lib/clamav:ro", "-v", "/:/host:ro",
		"--entrypoint", "clamscan", avImage, // the image's own entrypoint chowns the (read-only) database first
		"--recursive", "--infected", "--cross-fs=no", "--max-filesize=100M", "--max-scansize=400M"}
	for _, d := range avDenied {
		args = append(args, "--exclude-dir=^/host"+d+"/")
	}
	for _, p := range paths {
		p = filepath.Clean(p)
		if !filepath.IsAbs(p) {
			return res, errors.New("not an absolute path: " + p)
		}
		for _, d := range avDenied {
			if p == d || strings.HasPrefix(p, d+"/") {
				return res, errs.New(400, "av.bad_path", "path", p)
			}
		}
		if _, err := os.Stat(p); err != nil {
			return res, errs.New(404, "files.not_found", "path", p)
		}
		args = append(args, "/host"+strings.TrimSuffix(p, "/"))
	}
	// the extension must be installed (the image); fresh signatures once a day
	if !avImagePresent(ctx) {
		return res, errs.New(409, "av.not_installed")
	}
	if fi, err := os.Stat(avUpdated); err != nil || time.Since(fi.ModTime()) > 24*time.Hour {
		freshclam(ctx, log)
	}
	if b, err := os.ReadFile(avUpdated); err == nil {
		res.DBDate = strings.TrimSpace(string(b))
	}
	log("clamscan " + strings.Join(paths, " "))
	start := time.Now()
	n, a := sysexec.Scoped("docker", args...)
	cmd := exec.CommandContext(ctx, n, a...)
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return res, err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	summary := false
	var tail []string
	for sc.Scan() {
		l := sc.Text()
		if tail = append(tail, l); len(tail) > 5 {
			tail = tail[1:]
		}
		if strings.HasSuffix(l, " FOUND") {
			i := strings.LastIndex(l, ": ")
			if i > 0 {
				hit := AVHit{Path: strings.TrimPrefix(l[:i], "/host"), Sig: strings.TrimSuffix(l[i+2:], " FOUND")}
				res.Infected = append(res.Infected, hit)
				log("FOUND " + hit.Path + "  " + hit.Sig)
			}
		} else if v, ok := strings.CutPrefix(l, "Scanned files: "); ok {
			res.Files, _ = strconv.Atoi(strings.TrimSpace(v))
			summary = true
		}
	}
	err := cmd.Wait()
	res.Seconds = int(time.Since(start).Seconds())
	// clamscan: 0 = clean, 1 = found something, 2 = an error (but the rest may still be scanned)
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		err = nil
	}
	if !summary { // it never got to scanning: say why instead of reporting a clean result
		return res, errors.New("clamscan did not finish: " + strings.Join(tail, " | "))
	}
	log(strconv.Itoa(res.Files) + " files scanned, " + strconv.Itoa(len(res.Infected)) + " infected")
	return res, nil
}

func avImagePresent(ctx context.Context) bool {
	out, _ := avCmd(ctx, nil, "image", "inspect", "-f", "{{.Id}}", avImage)
	return strings.HasPrefix(strings.TrimSpace(out), "sha256:")
}

func avVolumePresent(ctx context.Context) bool {
	_, err := avCmd(ctx, nil, "volume", "inspect", avVolume)
	return err == nil
}

// freshclam updates the signatures in the volume.
func freshclam(ctx context.Context, log func(string)) error {
	log("freshclam (updating virus signatures)")
	avCmd(ctx, log, "volume", "create", avVolume)
	out, err := avCmd(ctx, log, "run", "--rm", "-v", avVolume+":/var/lib/clamav", avImage, "freshclam", "--stdout", "--no-warnings")
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(l, "updated") || strings.Contains(l, "up-to-date") || strings.Contains(l, "ERROR") {
			log(strings.TrimSpace(l))
		}
	}
	if err != nil {
		log("signature update failed; scanning with the signatures already there")
		return err
	}
	return os.WriteFile(avUpdated, []byte(time.Now().Format(time.RFC3339)), 0o644)
}

// AVStatus: whether the virus-scan extension is installed (the image is here) and how fresh
// its signatures are.
type AVStatus struct {
	Installed bool   `json:"installed"`
	Volume    bool   `json:"volume"`
	DBDate    string `json:"db_date,omitempty"`
	Image     string `json:"image"`
}

func AVState(ctx context.Context) AVStatus {
	st := AVStatus{Installed: avImagePresent(ctx), Volume: avVolumePresent(ctx), Image: avImage}
	if b, err := os.ReadFile(avUpdated); err == nil {
		st.DBDate = strings.TrimSpace(string(b))
	}
	return st
}

// AVInstall pulls the image and downloads the signatures.
func AVInstall(ctx context.Context, log func(string)) error {
	if !avImagePresent(ctx) {
		log("docker pull " + avImage + " (about 560 MB)")
		if out, err := avCmd(ctx, log, "pull", "-q", avImage); err != nil {
			return errs.New(500, "av.install_failed", "detail", strings.TrimSpace(out))
		}
	}
	if err := freshclam(ctx, log); err != nil {
		return errs.New(500, "av.install_failed", "detail", "freshclam: "+err.Error())
	}
	return nil
}

// AVRemove deletes the image, the signatures and the update stamp.
func AVRemove(ctx context.Context, log func(string)) error {
	if out, _ := avCmd(ctx, log, "ps", "-q", "--filter", "name=berth-clamav-"); strings.TrimSpace(out) != "" {
		return errs.New(409, "av.scanning")
	}
	log("docker rmi " + avImage)
	if out, err := avCmd(ctx, log, "rmi", avImage); err != nil && avImagePresent(ctx) {
		return errs.New(500, "av.remove_failed", "detail", strings.TrimSpace(out))
	}
	log("docker volume rm " + avVolume)
	avCmd(ctx, log, "volume", "rm", avVolume)
	os.Remove(avUpdated)
	return nil
}

func registerAV(s *Server) {
	s.Handle("GET /v1/av/status", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		WriteJSON(w, 200, AVState(r.Context()))
	})
	for _, op := range []struct {
		path string
		fn   func(context.Context, func(string)) error
	}{{"/v1/av/install", AVInstall}, {"/v1/av/remove", AVRemove}} {
		fn := op.fn
		s.Handle("POST "+op.path, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			st := NewStream(w)
			st.End(fn(r.Context(), func(l string) { st.Send(map[string]string{"l": l}) }))
		})
	}
	s.Handle("POST /v1/av/scan", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Paths []string `json:"paths"`
		}
		json.Unmarshal(body, &in)
		st := NewStream(w)
		log := func(l string) { st.Send(map[string]string{"l": l}) }
		res, err := Scan(r.Context(), in.Paths, log)
		if err == nil {
			b, _ := json.Marshal(res)
			log("::result " + string(b))
		}
		st.End(err)
	})
}
