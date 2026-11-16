package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const lookMarker = ".config/berth-look-2"

// applyLook gives the desktop user a tidier Xfce once, when the "look" item is installed:
// Adwaita-style theme, Papirus icons, a top bar with the Whisker menu and a small dock with
// the everyday programs. Earlier settings are kept next to it as *.berth-bak.
func applyLook(home string, uid, gid int, log func(string)) {
	if _, err := os.Stat(filepath.Join(home, lookMarker)); err == nil {
		return
	}
	if _, err := os.Stat("/usr/share/icons/Papirus"); err != nil {
		return // the look item is not installed
	}
	log("applying the Berth look for the desktop user")
	xml := filepath.Join(home, ".config/xfce4/xfconf/xfce-perchannel-xml")
	theme := "Adwaita"
	if _, err := os.Stat("/usr/share/themes/adw-gtk3"); err == nil {
		theme = "adw-gtk3"
	}
	write := func(path, body string) {
		os.MkdirAll(filepath.Dir(path), 0o755)
		if _, err := os.Stat(path); err == nil {
			os.Rename(path, path+".berth-bak")
		}
		if os.WriteFile(path, []byte(body), 0o644) == nil {
			chownUp(home, path, uid, gid)
		}
	}
	write(filepath.Join(xml, "xsettings.xml"), fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<channel name="xsettings" version="1.0">
  <property name="Net" type="empty">
    <property name="ThemeName" type="string" value="%s"/>
    <property name="IconThemeName" type="string" value="Papirus"/>
  </property>
  <property name="Gtk" type="empty">
    <property name="FontName" type="string" value="Noto Sans CJK SC 10"/>
    <property name="MonospaceFontName" type="string" value="DejaVu Sans Mono 10"/>
  </property>
  <property name="Xft" type="empty">
    <property name="Antialias" type="int" value="1"/>
    <property name="HintStyle" type="string" value="hintslight"/>
    <property name="RGBA" type="string" value="rgb"/>
  </property>
</channel>
`, theme))
	write(filepath.Join(xml, "xfwm4.xml"), `<?xml version="1.0" encoding="UTF-8"?>
<channel name="xfwm4" version="1.0">
  <property name="general" type="empty">
    <property name="theme" type="string" value="Default-xhdpi"/>
    <property name="title_font" type="string" value="Noto Sans CJK SC Bold 10"/>
    <property name="use_compositing" type="bool" value="false"/>
  </property>
</channel>
`)
	// dock launchers: whatever of these is installed
	var launchers []string
	// one per slot, the first name that exists (names differ between distributions)
	slots := [][]string{
		{"xfce4-terminal.desktop", "xfce4-terminal-emulator.desktop"},
		{"thunar.desktop", "xfce4-file-manager.desktop"},
		{"org.mozilla.firefox.desktop", "firefox.desktop", "firefox-esr.desktop"},
		{"chromium-browser.desktop", "chromium.desktop"},
		{"org.xfce.mousepad.desktop"},
		{"geany.desktop", "org.geany.Geany.desktop"},
		{"org.wireshark.Wireshark.desktop", "wireshark.desktop"},
		{"xfce4-taskmanager.desktop"},
	}
	for _, slot := range slots {
		for _, d := range slot {
			if b, err := os.ReadFile(filepath.Join("/usr/share/applications", d)); err == nil {
				id := 20 + len(launchers)
				write(filepath.Join(home, ".config/xfce4/panel", fmt.Sprintf("launcher-%d", id), d), string(b))
				launchers = append(launchers, fmt.Sprintf("%d:%s", id, d))
				break
			}
		}
	}
	var ids, plugins strings.Builder
	for _, l := range launchers {
		id, file, _ := strings.Cut(l, ":")
		fmt.Fprintf(&ids, `<value type="int" value="%s"/>`, id)
		fmt.Fprintf(&plugins, `
    <property name="plugin-%s" type="string" value="launcher">
      <property name="items" type="array"><value type="string" value="%s"/></property>
    </property>`, id, file)
	}
	write(filepath.Join(xml, "xfce4-panel.xml"), fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<channel name="xfce4-panel" version="1.0">
  <property name="configver" type="int" value="2"/>
  <property name="panels" type="array">
    <value type="int" value="1"/>
    <value type="int" value="2"/>
    <property name="dark-mode" type="bool" value="true"/>
    <property name="panel-1" type="empty">
      <property name="position" type="string" value="p=6;x=0;y=0"/>
      <property name="length" type="uint" value="100"/>
      <property name="position-locked" type="bool" value="true"/>
      <property name="size" type="uint" value="30"/>
      <property name="plugin-ids" type="array">
        <value type="int" value="1"/><value type="int" value="2"/><value type="int" value="3"/><value type="int" value="4"/><value type="int" value="5"/>
      </property>
    </property>
    <property name="panel-2" type="empty">
      <property name="position" type="string" value="p=10;x=0;y=0"/>
      <property name="length" type="uint" value="1"/>
      <property name="position-locked" type="bool" value="true"/>
      <property name="size" type="uint" value="44"/>
      <property name="autohide-behavior" type="uint" value="1"/>
      <property name="plugin-ids" type="array">%s</property>
    </property>
  </property>
  <property name="plugins" type="empty">
    <property name="plugin-1" type="string" value="whiskermenu"/>
    <property name="plugin-2" type="string" value="tasklist">
      <property name="flat-buttons" type="bool" value="true"/>
    </property>
    <property name="plugin-3" type="string" value="separator">
      <property name="expand" type="bool" value="true"/>
      <property name="style" type="uint" value="0"/>
    </property>
    <property name="plugin-4" type="string" value="systray">
      <property name="square-icons" type="bool" value="true"/>
    </property>
    <property name="plugin-5" type="string" value="clock">
      <property name="digital-layout" type="uint" value="3"/>
      <property name="digital-time-format" type="string" value="%%m-%%d %%H:%%M"/>
    </property>%s
  </property>
</channel>
`, ids.String(), plugins.String()))
	write(filepath.Join(home, lookMarker), "applied by Berth; delete to apply again on the next start\n")
}

// chownUp gives the user the file and every folder created for it under home.
func chownUp(home, path string, uid, gid int) {
	for p := path; strings.HasPrefix(p, home+"/"); p = filepath.Dir(p) {
		os.Chown(p, uid, gid)
	}
}
