package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agentproto"
	"github.com/wangsirco/berth/internal/panel"
	"github.com/wangsirco/berth/internal/panel/certkit"
	"github.com/wangsirco/berth/internal/store"
	"github.com/wangsirco/berth/internal/tlsutil"
	"github.com/wangsirco/berth/internal/version"
	"github.com/wangsirco/berth/web"
)

func main() {
	// berth-panel admin …: the rescue tool for SSH sessions (berth admin calls it)
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		etc, args := "/etc/berth", os.Args[2:]
		if len(args) > 1 && args[0] == "-etc" {
			etc, args = args[1], args[2:]
		}
		cfg, err := loadConfig(etc)
		if err == nil {
			var db *sql.DB
			if db, err = store.Open(filepath.Join(cfg.DataDir, "berth.db")); err == nil {
				err = panel.AdminCLI(db, cfg, args)
				db.Close()
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "berth admin:", err)
			os.Exit(1)
		}
		return
	}
	etc := flag.String("etc", "/etc/berth", "config directory")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println("berth-panel", version.Version)
		return
	}
	// 8 GB machines: keep the heap small (方案 §1.3, panel + agent ≤ 100 MB).
	debug.SetGCPercent(50)
	debug.SetMemoryLimit(48 << 20)
	if err := run(*etc); err != nil {
		log.Fatalf("berth-panel: %v", err)
	}
}

type panelKey struct {
	KeyID  string `json:"key_id"`
	Secret string `json:"secret"`
	Socket string `json:"socket"`
}

func loadConfig(etc string) (panel.Config, error) {
	cfg := panel.Config{Listen: ":7090", DataDir: "/var/lib/berth", EtcDir: etc}
	b, err := os.ReadFile(filepath.Join(etc, "panel.json"))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("panel.json: %w", err)
	}
	cfg.EtcDir = etc
	return cfg, nil
}

func run(etc string) error {
	cfg, err := loadConfig(etc)
	if err != nil {
		return err
	}
	db, err := store.Open(filepath.Join(cfg.DataDir, "berth.db"))
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(etc, "panel-agent.key"))
	if err != nil {
		return fmt.Errorf("read agent key (is berth-agent installed and started?): %w", err)
	}
	var pk panelKey
	if err := json.Unmarshal(b, &pk); err != nil {
		return err
	}
	secret, err := base64.StdEncoding.DecodeString(pk.Secret)
	if err != nil {
		return err
	}
	local := agentproto.NewClient(pk.Socket, pk.KeyID, secret)
	var mk struct{ Key []byte }
	for i := 0; ; i++ { // the agent may still be starting
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = local.Do(ctx, "GET", "/v1/masterkey", nil, &mk)
		cancel()
		if err == nil || i == 30 {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		return fmt.Errorf("get master key from agent: %w", err)
	}
	webFS, err := fs.Sub(web.FS, "dist")
	if err != nil {
		return err
	}
	app := panel.New(cfg, db, map[string]*agentproto.Client{"local": local}, webFS)
	app.SetMasterKey(mk.Key)

	ca, err := tlsutil.OpenLocalCA(filepath.Join(cfg.DataDir, "ca"), "Berth")
	if err != nil {
		return err
	}
	certNames := func() []string {
		h, _ := os.Hostname()
		base := strings.ToLower(strings.Split(h, ".")[0])
		names := []string{"localhost"}
		if base != "" && base != "localhost" {
			names = append(names, base+".local", base+".lan", base+".home.arpa") // mDNS (avahi) and common LAN zones
		}
		return append(names, app.CertNames()...)
	}
	if err := ca.Refresh(certNames()); err != nil {
		return err
	}
	go ca.Watch(certNames, log.Printf)
	app.SetCA(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.CADER()}), ca.CAFingerprint())
	caCert, caKey := ca.SigningCA()
	app.SetLocalSigner(certkit.Signer{Cert: caCert, Key: caKey, Allow: ca.Permits})

	_, port, _ := net.SplitHostPort(cfg.Listen)
	var urls []string
	for _, a := range reachableAddrs() {
		urls = append(urls, "https://"+net.JoinHostPort(a, port))
	}
	app.AnnounceSetup(urls)
	app.Start(context.Background())

	srv := &http.Server{
		Addr: cfg.Listen, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: ca.GetCertificate},
		ErrorLog:  log.New(os.Stderr, "http: ", 0),
	}
	log.Printf("berth-panel %s listening on %s (CA %s)", version.Version, cfg.Listen, ca.CAFingerprint())
	return srv.ListenAndServeTLS("", "")
}

// reachableAddrs lists addresses people can open the panel on: no loopback, no
// container bridges (docker0, br-*, veth*), no IPv6 link-local.
func reachableAddrs() []string {
	ifs, _ := net.Interfaces()
	var out []string
	for _, ifc := range ifs {
		n := ifc.Name
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 ||
			strings.HasPrefix(n, "docker") || strings.HasPrefix(n, "br-") || strings.HasPrefix(n, "veth") {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ipn.IP.String())
		}
	}
	return out
}
