package panel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/release"
	"github.com/wangsirco/berth/internal/version"
)

type fakeRelease struct {
	srv      *httptest.Server
	manifest []byte
	sig      string
	asset    []byte
}

func newFakeRelease(t *testing.T, ver string, tamper bool) *fakeRelease {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f := &fakeRelease{asset: []byte("tarball-bytes")}
	sum := sha256.Sum256(f.asset)
	m := release.Manifest{Version: ver, Released: time.Now().Unix(), Notes: map[string]string{"zh-CN": "修复了一些问题", "en": "Fixes"}}
	m.Core.Hash = "core-2"
	m.Core.Packages = map[string]release.Asset{"arm64": {Name: "berth_arm64.tar.gz", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(f.asset))}}
	f.manifest, _ = json.Marshal(m)
	f.sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, f.manifest))
	if tamper {
		f.manifest[len(f.manifest)-2] ^= 1
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + release.ManifestName:
			w.Write(f.manifest)
		case "/" + release.SigName:
			w.Write([]byte(f.sig))
		case "/berth_arm64.tar.gz":
			w.Write(f.asset)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	oldKey, oldVer := version.ReleasePubKey, version.Version
	version.ReleasePubKey, version.Version = base64.StdEncoding.EncodeToString(pub), "0.1.9"
	t.Cleanup(func() { version.ReleasePubKey, version.Version = oldKey, oldVer })
	return f
}

func TestCheckUpdate(t *testing.T) {
	c := newTC(t)
	f := newFakeRelease(t, "0.1.10", false)
	c.app.setSetting("update.url", f.srv.URL)
	info, err := c.app.checkUpdate(context.Background())
	if err != nil || !info.Available || info.Latest != "0.1.10" || info.WebOnly {
		t.Fatalf("%+v %v", info, err)
	}
	as := info.manifest.Core.Packages["arm64"]
	if _, err := download(context.Background(), f.srv.URL, as, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	as.SHA256 = "00"
	if _, err := download(context.Background(), f.srv.URL, as, t.TempDir()); err == nil || err.Error() != "update.bad_checksum" {
		t.Fatalf("checksum: %v", err)
	}
}

func TestTamperedManifestRefused(t *testing.T) {
	c := newTC(t)
	f := newFakeRelease(t, "0.1.10", true)
	c.app.setSetting("update.url", f.srv.URL)
	if _, err := c.app.checkUpdate(context.Background()); err == nil || err.Error() != "update.bad_signature" {
		t.Fatalf("got %v", err)
	}
}

func TestStableIgnoresBeta(t *testing.T) {
	c := newTC(t)
	f := newFakeRelease(t, "0.2.0-beta.1", false)
	c.app.setSetting("update.url", f.srv.URL)
	info, _ := c.app.checkUpdate(context.Background())
	if info.Available {
		t.Fatal("beta offered on stable")
	}
	c.app.setSetting("update.channel", "beta")
	if info, _ = c.app.checkUpdate(context.Background()); !info.Available {
		t.Fatal("beta channel should offer it")
	}
}

func TestVersionOrder(t *testing.T) {
	for _, tc := range [][3]any{{"0.1.10", "0.1.9", 1}, {"0.2.0-beta.1", "0.2.0", -1}, {"0.2.0", "0.2.0", 0}, {"1.0.0", "0.9.9", 1}, {"0.2.0-beta.2", "0.2.0-beta.1", 1}} {
		if got := cmpVersion(tc[0].(string), tc[1].(string)); got != tc[2].(int) {
			t.Errorf("%v vs %v = %d", tc[0], tc[1], got)
		}
	}
	if !inWindow("23:00-05:00", time.Date(2026, 1, 1, 2, 0, 0, 0, time.Local)) || inWindow("03:00-05:00", time.Date(2026, 1, 1, 6, 0, 0, 0, time.Local)) {
		t.Fatal("window")
	}
}

func TestUpdateNotesSizesAndStatus(t *testing.T) {
	c := newTC(t)
	f := newFakeRelease(t, "0.1.10", false)
	c.app.setSetting("update.url", f.srv.URL)
	info, err := c.app.checkUpdate(context.Background())
	if err != nil || info.Notes["zh-CN"] != "修复了一些问题" || info.CoreBytes != int64(len(f.asset)) || info.Checked == 0 {
		t.Fatalf("%+v %v", info, err)
	}
	if l := lastCheck.Load(); l == nil || l.Latest != "0.1.10" {
		t.Fatalf("last check not kept: %+v", l)
	}
}

func TestWebReset(t *testing.T) {
	c := newTC(t)
	if c.app.webOverridden() {
		t.Fatal("fresh panel has no downloaded UI")
	}
	os.MkdirAll(c.app.webDir(), 0o750)
	os.WriteFile(filepath.Join(c.app.webDir(), ".core"), []byte("x"), 0o640)
	if !c.app.webOverridden() {
		t.Fatal("override not seen")
	}
	ch, err := c.app.pipe.Run(context.Background(), change.Actor{Type: "system", ID: "test", Name: "test"}, "local", "panel.web_reset", []byte(`{}`))
	if err != nil || ch.Status != "done" || c.app.webOverridden() {
		t.Fatalf("%+v %v", ch, err)
	}
}
