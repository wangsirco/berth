// mkmanifest writes and signs berth-manifest.json for a release directory.
//
//	BERTH_SIGN_SEED=<base64 ed25519 seed> go run ./scripts/mkmanifest -dir dist-release/0.1.0 -version 0.1.0
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/release"
)

func asset(dir, name string) release.Asset {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		fail(err)
	}
	s := sha256.Sum256(b)
	return release.Asset{Name: name, SHA256: hex.EncodeToString(s[:]), Size: int64(len(b))}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "mkmanifest:", err)
	os.Exit(1)
}

func main() {
	dir := flag.String("dir", "", "release directory")
	ver := flag.String("version", "", "version")
	webHash := flag.String("web-hash", "", "release.WebHash of web/dist")
	coreHash := flag.String("core-hash", "", "release.CoreHash of the sources")
	notes := flag.String("notes", "", "JSON file of release notes per UI language: {\"zh-CN\": \"…\", \"en\": \"…\"}")
	flag.Parse()
	m := release.Manifest{Version: *ver, Channel: "stable", Released: time.Now().Unix()}
	if *notes != "" {
		b, err := os.ReadFile(*notes)
		if err != nil {
			log.Fatal(err)
		}
		if err := json.Unmarshal(b, &m.Notes); err != nil {
			log.Fatal("notes: ", err)
		}
	}
	if strings.Contains(*ver, "-") {
		m.Channel = "beta"
	}
	m.Web.Hash, m.Core.Hash = *webHash, *coreHash
	m.Web.Asset = asset(*dir, "berth_"+*ver+"_web.tar.gz")
	m.Core.Packages = map[string]release.Asset{}
	for _, arch := range []string{"amd64", "arm64"} {
		name := fmt.Sprintf("berth_%s_linux_%s.tar.gz", *ver, arch)
		if _, err := os.Stat(filepath.Join(*dir, name)); err == nil { // only the architectures built
			m.Core.Packages[arch] = asset(*dir, name)
		}
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	sig, err := release.Sign(b, os.Getenv("BERTH_SIGN_SEED"))
	if err != nil {
		fail(err)
	}
	must(os.WriteFile(filepath.Join(*dir, release.ManifestName), b, 0o644))
	must(os.WriteFile(filepath.Join(*dir, release.SigName), []byte(sig+"\n"), 0o644))
}

func must(err error) {
	if err != nil {
		fail(err)
	}
}
