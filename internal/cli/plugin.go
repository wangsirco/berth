package cli

// berth plugin: the plugin developer kit — a template, checking, packing and signing.
// (berth plugin install / remove are the panel's changes, like every other command.)

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wangsirco/berth/internal/plugin"
)

const pluginHelp = `berth plugin — make Berth plugins (see docs/plugins/PLUGIN-GUIDE.md)

  berth plugin init <dir> [--id ID] [--native|--container]   a working template
  berth plugin check <dir>                                    check the manifest and files
  berth plugin pack <dir> [-o out.berthplugin] [--key FILE]   make the package (signed with --key)
  berth plugin keygen <file>                                  a signing key (keep it private)
  berth plugin fingerprint <file.berthplugin|key file>        the signer's fingerprint
  berth plugin install|remove …                               on a panel, like other commands
`

func cmdPlugin(args []string) error {
	if len(args) == 0 {
		fmt.Print(pluginHelp)
		return nil
	}
	switch args[0] {
	case "init":
		return pluginInit(args[1:])
	case "check":
		if len(args) != 2 {
			return errors.New("usage: berth plugin check <dir>")
		}
		_, p, err := plugin.Pack(args[1], nil)
		if err != nil {
			return err
		}
		fmt.Printf("ok: %s %s (%s), %d files\n", p.Manifest.ID, p.Manifest.Version, p.Manifest.Runtime.Type, len(p.Files))
		return nil
	case "pack":
		return pluginPack(args[1:])
	case "keygen":
		if len(args) != 2 {
			return errors.New("usage: berth plugin keygen <file>")
		}
		if _, err := os.Stat(args[1]); err == nil {
			return errors.New(args[1] + " exists; not overwriting a key")
		}
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		if err := os.WriteFile(args[1], []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Printf("key written to %s (keep it private and backed up)\nfingerprint: %s\n", args[1], plugin.Fingerprint(base64.StdEncoding.EncodeToString(pub)))
		return nil
	case "fingerprint":
		if len(args) != 2 {
			return errors.New("usage: berth plugin fingerprint <file>")
		}
		if strings.HasSuffix(args[1], ".berthplugin") {
			b, err := os.ReadFile(args[1])
			if err != nil {
				return err
			}
			p, err := plugin.Read(b, nil)
			if err != nil {
				return err
			}
			if !p.Signed {
				fmt.Println("not signed")
				return nil
			}
			fmt.Printf("%s (signature %s)\n", plugin.Fingerprint(p.Signer), map[bool]string{true: "valid", false: "INVALID"}[p.SigOK])
			return nil
		}
		priv, err := readKey(args[1])
		if err != nil {
			return err
		}
		fmt.Println(plugin.Fingerprint(base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))))
		return nil
	case "install", "remove":
		return runGenerated(append([]string{"plugin"}, args...))
	}
	return errors.New("unknown: berth plugin " + args[0] + "\n" + pluginHelp)
}

func readKey(file string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New(file + " is not a key made by `berth plugin keygen`")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func pluginPack(args []string) error {
	var dir, out, key string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o":
			i++
			if i < len(args) {
				out = args[i]
			}
		case "--key":
			i++
			if i < len(args) {
				key = args[i]
			}
		default:
			dir = args[i]
		}
	}
	if dir == "" {
		return errors.New("usage: berth plugin pack <dir> [-o out.berthplugin] [--key FILE]")
	}
	var priv ed25519.PrivateKey
	if key != "" {
		var err error
		if priv, err = readKey(key); err != nil {
			return err
		}
	}
	b, p, err := plugin.Pack(dir, priv)
	if err != nil {
		return err
	}
	if out == "" {
		out = p.Manifest.ID + "-" + p.Manifest.Version + ".berthplugin"
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("%s: %d KB, sha256 %s\n", out, len(b)/1024, p.SHA256)
	if p.Signed {
		fmt.Println("signed by", plugin.Fingerprint(p.Signer))
	} else {
		fmt.Println("not signed (installers see a yellow warning; root plugins must be signed)")
	}
	return nil
}

func pluginInit(args []string) error {
	dir, id, kind := "", "", "native"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--id":
			i++
			if i < len(args) {
				id = args[i]
			}
		case "--native":
			kind = "native"
		case "--container":
			kind = "container"
		default:
			dir = args[i]
		}
	}
	if dir == "" {
		return errors.New("usage: berth plugin init <dir> [--id ID] [--native|--container]")
	}
	if id == "" {
		id = strings.ToLower(filepath.Base(dir))
	}
	if !plugin.IDRe.MatchString(id) {
		return fmt.Errorf("%q is not a plugin id (lowercase letters, digits and -)", id)
	}
	if _, err := os.Stat(filepath.Join(dir, plugin.ManifestName)); err == nil {
		return errors.New(dir + " already has a plugin")
	}
	files := map[string]string{}
	if kind == "native" {
		files[plugin.ManifestName] = fmt.Sprintf(nativeTemplate, id)
		files["main.go"] = nativeMain
		files["Makefile"] = fmt.Sprintf("build:\n\tGOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o bin/%s-arm64 .\n\tGOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/%s-amd64 .\n", id, id)
		files["go.mod"] = "module " + id + "\n\ngo 1.24\n"
	} else {
		files[plugin.ManifestName] = fmt.Sprintf(containerTemplate, id)
		files["Dockerfile"] = "FROM python:3.12-alpine\nWORKDIR /app\nCOPY app.py .\nUSER 1000\nCMD [\"python\", \"app.py\"]\n"
		files["app.py"] = containerApp
	}
	files["icon.svg"] = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48"><rect width="48" height="48" rx="10" fill="#1f6f5c"/><path d="M14 30h20" stroke="#f7f5f0" stroke-width="4" stroke-linecap="round"/></svg>` + "\n"
	for n, c := range files {
		p := filepath.Join(dir, n)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("made %s (%s plugin %s). Next:\n", dir, kind, id)
	if kind == "native" {
		fmt.Printf("  cd %s && make && berth plugin pack . --key ~/berth-plugin.key\n", dir)
	} else {
		fmt.Printf("  berth plugin pack %s --key ~/berth-plugin.key\n", dir)
	}
	return nil
}

const nativeTemplate = `api: 1
id: %[1]s
name:
  zh-CN: 我的插件
  en: My plugin
version: 0.1.0
description:
  zh-CN: 一句话说明它能做什么。
  en: One sentence on what it does.
author: Your name
icon: icon.svg
runtime:
  type: native
  binary:
    arm64: bin/%[1]s-arm64
    amd64: bin/%[1]s-amd64
  page: true          # serves a page on $PORT (127.0.0.1), shown inside the panel
  run_as: plugin      # its own user and a read-only system; "root" needs a signature
permissions:          # Berth API scopes it asks for; the installer may grant fewer
  - overview:read
settings:
  - key: greeting
    label: {zh-CN: 问候语, en: Greeting}
    type: text
    default: hello
`

const nativeMain = `package main

// A native Berth plugin: a page on $PORT that calls the Berth API with $BERTH_TOKEN.

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
)

func main() {
	pool := x509.NewCertPool()
	if ca, err := os.ReadFile(os.Getenv("BERTH_CA_FILE")); err == nil {
		pool.AppendCertsFromPEM(ca)
	}
	berth := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequest("GET", os.Getenv("BERTH_URL")+"/api/v1/hosts", nil)
		req.Header.Set("Authorization", "Bearer "+os.Getenv("BERTH_TOKEN"))
		body := "(no answer)"
		if resp, err := berth.Do(req); err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = string(b)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<h1>%s</h1><pre>%s</pre>", os.Getenv("BERTH_SETTING_GREETING"), body)
	})
	http.ListenAndServe("127.0.0.1:"+os.Getenv("PORT"), nil)
}
`

const containerTemplate = `api: 1
id: %[1]s
name:
  zh-CN: 我的插件
  en: My plugin
version: 0.1.0
description:
  zh-CN: 一句话说明它能做什么。
  en: One sentence on what it does.
author: Your name
icon: icon.svg
runtime:
  type: container
  build: .            # or image: ghcr.io/you/plugin:0.1.0
  port: 8080          # where its page listens inside the container
permissions:
  - overview:read
`

const containerApp = `# A container Berth plugin: a page on :8080 inside the container.
import os
from http.server import BaseHTTPRequestHandler, HTTPServer

class H(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.end_headers()
        self.wfile.write(f"<h1>Hello from {os.environ.get('BERTH_PLUGIN')}</h1>".encode())

HTTPServer(("0.0.0.0", 8080), H).serve_forever()
`
