package desktop

import (
	"context"
	"strings"
)

// Item is one thing the desktop can have, picked in the panel. Packages differ between
// Fedora (dnf) and Debian/Ubuntu (apt); the first package is the one checked for "installed".
type Item struct {
	ID     string   `json:"id"`
	Group  string   `json:"group"`  // apps | dev | net | look
	Preset string   `json:"preset"` // basic | dev | "" (only when picked)
	SizeMB int      `json:"size_mb"`
	DNF    []string `json:"-"`
	APT    []string `json:"-"`
	Have   bool     `json:"installed"`
}

// Items checked on Fedora 45 aarch64 (2026-11-13). Labels live in the panel's strings
// (desktop.item.<id>).
var Items = []Item{
	// looks: always part of the desktop
	{ID: "look", Group: "look", Preset: "basic", SizeMB: 60, DNF: []string{"papirus-icon-theme", "adw-gtk3-theme", "xfce4-whiskermenu-plugin", "google-noto-sans-cjk-vf-fonts", "glibc-langpack-zh"},
		APT: []string{"papirus-icon-theme", "xfce4-whiskermenu-plugin", "fonts-noto-cjk", "locales-all"}},
	// everyday software
	{ID: "firefox", Group: "apps", Preset: "basic", SizeMB: 260, DNF: []string{"firefox"}, APT: []string{"firefox-esr"}},
	{ID: "chromium", Group: "apps", SizeMB: 330, DNF: []string{"chromium"}, APT: []string{"chromium"}},
	{ID: "editor", Group: "apps", Preset: "basic", SizeMB: 5, DNF: []string{"mousepad"}, APT: []string{"mousepad"}},
	{ID: "files", Group: "apps", Preset: "basic", SizeMB: 15, DNF: []string{"file-roller", "ristretto", "atril"}, APT: []string{"file-roller", "ristretto", "atril"}},
	{ID: "tools", Group: "apps", Preset: "basic", SizeMB: 10, DNF: []string{"xfce4-screenshooter", "xfce4-taskmanager"}, APT: []string{"xfce4-screenshooter", "xfce4-taskmanager"}},
	{ID: "ime", Group: "apps", Preset: "basic", SizeMB: 80, DNF: []string{"fcitx5-chinese-addons", "fcitx5", "fcitx5-configtool", "fcitx5-gtk"}, APT: []string{"fcitx5-chinese-addons", "fcitx5", "fcitx5-config-qt"}},
	{ID: "office", Group: "apps", SizeMB: 450, DNF: []string{"libreoffice-writer", "libreoffice-calc"}, APT: []string{"libreoffice-writer", "libreoffice-calc"}},
	{ID: "remote", Group: "apps", SizeMB: 40, DNF: []string{"remmina", "filezilla"}, APT: []string{"remmina", "filezilla"}},
	// development
	{ID: "git", Group: "dev", Preset: "dev", SizeMB: 40, DNF: []string{"git", "gitg", "meld"}, APT: []string{"git", "gitg", "meld"}},
	{ID: "geany", Group: "dev", Preset: "dev", SizeMB: 15, DNF: []string{"geany"}, APT: []string{"geany"}},
	{ID: "cli", Group: "dev", Preset: "dev", SizeMB: 60, DNF: []string{"tmux", "htop", "btop", "vim-enhanced", "neovim", "jq", "ripgrep", "fd-find", "tree"},
		APT: []string{"tmux", "htop", "btop", "vim", "neovim", "jq", "ripgrep", "fd-find", "tree"}},
	{ID: "build", Group: "dev", Preset: "dev", SizeMB: 180, DNF: []string{"gcc", "make", "cmake"}, APT: []string{"build-essential", "cmake"}},
	{ID: "python", Group: "dev", Preset: "dev", SizeMB: 30, DNF: []string{"python3-pip"}, APT: []string{"python3-pip", "python3-venv"}},
	{ID: "node", Group: "dev", Preset: "dev", SizeMB: 90, DNF: []string{"nodejs", "nodejs-npm"}, APT: []string{"nodejs", "npm"}},
	{ID: "go", Group: "dev", SizeMB: 250, DNF: []string{"golang"}, APT: []string{"golang"}},
	{ID: "rust", Group: "dev", SizeMB: 400, DNF: []string{"rust", "cargo"}, APT: []string{"rustc", "cargo"}},
	{ID: "java", Group: "dev", SizeMB: 300, DNF: []string{"java-25-openjdk-devel"}, APT: []string{"default-jdk"}},
	{ID: "dbclients", Group: "dev", SizeMB: 30, DNF: []string{"sqlite", "postgresql", "mariadb"}, APT: []string{"sqlite3", "postgresql-client", "mariadb-client"}},
	// network tools
	{ID: "nettools", Group: "net", Preset: "dev", SizeMB: 30, DNF: []string{"iperf3", "mtr", "nmap", "bind-utils", "traceroute", "nethogs", "iftop", "socat", "nmap-ncat", "tcpdump"},
		APT: []string{"iperf3", "mtr-tiny", "nmap", "dnsutils", "traceroute", "nethogs", "iftop", "socat", "netcat-openbsd", "tcpdump"}},
	{ID: "wireshark", Group: "net", Preset: "dev", SizeMB: 120, DNF: []string{"wireshark"}, APT: []string{"wireshark"}},
}

func (it Item) pkgs() []string {
	if has("dnf") {
		return it.DNF
	}
	return it.APT
}

// ItemsWithState marks what is installed (by the first package of each).
func ItemsWithState(ctx context.Context) []Item {
	mgr := "apt"
	if has("dnf") {
		mgr = "dnf"
	}
	out := make([]Item, len(Items))
	for i, it := range Items {
		it.Have = len(it.pkgs()) > 0 && installed(ctx, mgr, it.pkgs()[0])
		out[i] = it
	}
	return out
}

// pickPackages turns item ids into packages (unknown ids are ignored).
func pickPackages(ids []string) []string {
	var out []string
	for _, id := range ids {
		for _, it := range Items {
			if it.ID == id {
				for _, p := range it.pkgs() {
					if !contains(out, p) {
						out = append(out, p)
					}
				}
			}
		}
	}
	return out
}

// PresetItems: the ids a preset selects ("dev" includes "basic").
func PresetItems(preset string) []string {
	var out []string
	for _, it := range Items {
		if it.Preset == "basic" || (preset == "dev" && it.Preset == "dev") {
			out = append(out, it.ID)
		}
	}
	return out
}

func validItems(ids []string) bool {
	for _, id := range ids {
		ok := false
		for _, it := range Items {
			ok = ok || it.ID == id
		}
		if !ok || strings.ContainsAny(id, " /") {
			return false
		}
	}
	return true
}
