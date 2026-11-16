package panel

// `berth admin` (berth-panel admin): rescue from an SSH session when the web page is out of
// reach — see the address, change or turn off the security entry, rename the administrator,
// set a new password. It writes the panel's database directly and restarts the panel so the
// change takes effect. Every line is shown in Chinese and English.

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wangsirco/berth/internal/agent"
)

type adminCLI struct {
	db   *sql.DB
	cfg  Config
	unit string
	in   *bufio.Reader
}

func say(zh, en string) { fmt.Println(zh); fmt.Println("  " + en) }

func sayf(zh, en string, a ...any) {
	fmt.Printf(zh+"\n", a...)
	fmt.Printf("  "+en+"\n", a...)
}

// AdminCLI runs the rescue tool on the panel's own database.
func AdminCLI(db *sql.DB, cfg Config, args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("请用 root 运行（sudo berth admin） / run it as root (sudo berth admin)")
	}
	unit := "berth-panel"
	if base := filepath.Base(cfg.EtcDir); base != "berth" {
		unit = "berth-panel" + strings.TrimPrefix(base, "berth")
	}
	c := &adminCLI{db: db, cfg: cfg, unit: unit, in: bufio.NewReader(os.Stdin)}
	if len(args) == 0 {
		return c.menu()
	}
	switch args[0] {
	case "info":
		return c.info()
	case "entry":
		v := ""
		if len(args) > 1 {
			v = args[1]
		}
		return c.entry(v, len(args) > 1)
	case "username":
		v := ""
		if len(args) > 1 {
			v = args[1]
		}
		return c.username(v)
	case "password":
		return c.password(len(args) > 1 && args[1] == "--random")
	case "public":
		if len(args) < 2 || (args[1] != "on" && args[1] != "off") {
			return errors.New("berth admin public on|off")
		}
		return c.public(args[1] == "on")
	case "help", "-h", "--help":
		c.help()
		return nil
	}
	c.help()
	return fmt.Errorf("未知命令 / unknown command: %s", args[0])
}

func (c *adminCLI) help() {
	fmt.Println(`berth admin                 交互菜单 / interactive menu
berth admin info            面板地址、安全入口、管理员 / address, security entry, administrators
berth admin entry /abc123   修改安全入口 / change the security entry
berth admin entry off       关闭安全入口 / turn the security entry off
berth admin username NAME   修改管理员用户名 / rename the administrator
berth admin password        修改管理员密码 / set the administrator's password
berth admin password --random   生成随机密码 / generate a random password
berth admin public on       开启公网访问（任何地址都能打开面板，需要安全入口）/ open the panel to the internet
berth admin public off      关闭公网访问（只允许内网）/ private networks only`)
}

func (c *adminCLI) ask(zh, en string) string {
	fmt.Printf("%s / %s: ", zh, en)
	s, _ := c.in.ReadString('\n')
	return strings.TrimSpace(s)
}

func (c *adminCLI) menu() error {
	for {
		fmt.Println()
		fmt.Println("========== Berth 面板管理 / Panel admin ==========")
		fmt.Println(" 1) 查看面板信息 / Show panel info")
		fmt.Println(" 2) 修改安全入口 / Change the security entry")
		fmt.Println(" 3) 关闭安全入口 / Turn the security entry off")
		fmt.Println(" 4) 修改管理员用户名 / Rename the administrator")
		fmt.Println(" 5) 修改管理员密码 / Set the administrator's password")
		fmt.Println(" 6) 生成随机密码 / Generate a random password")
		if c.setting("access.public") == "1" {
			fmt.Println(" 7) 关闭公网访问 / Close public access")
		} else {
			fmt.Println(" 7) 开启公网访问 / Open public access")
		}
		fmt.Println(" 0) 退出 / Quit")
		var err error
		switch c.ask("请选择", "Choose") {
		case "1":
			err = c.info()
		case "2":
			err = c.entry(c.ask("新的安全入口（如 /mypanel88，留空随机生成）", "New entry (e.g. /mypanel88; empty = random)"), true)
		case "3":
			err = c.entry("off", true)
		case "4":
			err = c.username("")
		case "5":
			err = c.password(false)
		case "6":
			err = c.password(true)
		case "7":
			err = c.public(c.setting("access.public") != "1")
		case "0", "q", "":
			return nil
		default:
			say("没有这个选项。", "No such option.")
		}
		if err != nil {
			fmt.Println("✗", err)
		}
	}
}

func (c *adminCLI) setting(key string) string {
	var v string
	c.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	return v
}

func (c *adminCLI) setSetting(key, v string) error {
	_, err := c.db.Exec(`INSERT INTO settings(key, value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, v)
	return err
}

func (c *adminCLI) restart() {
	if out, err := exec.Command("systemctl", "try-restart", c.unit).CombinedOutput(); err != nil {
		sayf("面板没能自动重启，请手动执行：systemctl restart %s（%s）", "The panel did not restart; run: systemctl restart %s (%s)", c.unit, strings.TrimSpace(string(out)))
		return
	}
	sayf("面板已重启（%s），新设置已生效。", "The panel restarted (%s); the new settings are in effect.", c.unit)
}

// addresses: how the panel is reached from the LAN.
func (c *adminCLI) addresses() []string {
	_, port, _ := net.SplitHostPort(c.cfg.Listen)
	if port == "" {
		port = "7090"
	}
	var out []string
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || strings.HasPrefix(ifc.Name, "docker") || strings.HasPrefix(ifc.Name, "br-") || strings.HasPrefix(ifc.Name, "veth") || strings.HasPrefix(ifc.Name, "incusbr") {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ip, _, err := net.ParseCIDR(a.String()); err == nil && ip.To4() != nil {
				out = append(out, "https://"+ip.String()+":"+port)
			}
		}
	}
	return out
}

func (c *adminCLI) info() error {
	entry := c.setting("access.entry")
	fmt.Println()
	say("面板地址：", "Panel address:")
	for _, u := range c.addresses() {
		fmt.Println("   " + u + entry)
	}
	if c.setting("access.public") == "1" || c.cfg.Public {
		say("公网访问：已开启（任何地址都能打开面板，靠安全入口和二步验证保护）", "Public access: on (any address may open the panel; the entry and two-step sign-in protect it)")
		if ip, _, _, err := agent.PublicIP(context.Background(), "4", agent.PublicIPSources["4"]); err == nil {
			_, port, _ := net.SplitHostPort(c.cfg.Listen)
			if port == "" {
				port = "7090"
			}
			sayf("公网地址：https://%s:%s%s", "Public address: https://%s:%s%s", ip, port, entry)
		}
	} else {
		say("公网访问：未开启（只有内网和 Tailscale 能打开面板；sudo berth admin public on 开启）", "Public access: off (private networks and Tailscale only; sudo berth admin public on)")
	}
	if entry == "" {
		say("安全入口：未启用（任何人都能看到登录页）", "Security entry: off (anyone can see the sign-in page)")
	} else {
		sayf("安全入口：%s（不带这个路径访问时，面板只回一个 404）", "Security entry: %s (without this path the panel answers 404)", entry)
	}
	if cidrs := c.setting("access.allow_cidrs"); cidrs != "" {
		sayf("允许访问的网段：%s", "Allowed networks: %s", strings.ReplaceAll(cidrs, "\n", ", "))
	}
	rows, err := c.db.Query(`SELECT username, role, disabled FROM users ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name, role string
		var disabled bool
		rows.Scan(&name, &role, &disabled)
		if role == RoleSuper {
			mark := ""
			if disabled {
				mark = " (已停用 / disabled)"
			}
			sayf("超级管理员：%s%s", "Super admin: %s%s", name, mark)
			n++
		}
	}
	if n == 0 {
		b, _ := os.ReadFile(filepath.Join(c.cfg.DataDir, "setup-url"))
		say("还没有创建管理员。打开下面的地址完成初始化（10 分钟内有效）：", "No administrator yet. Open this link to set up the panel (valid for 10 minutes):")
		fmt.Print(string(b))
	}
	return nil
}

func randomString(n int, alphabet string) string {
	b := make([]byte, n)
	for i := range b {
		k, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b[i] = alphabet[k.Int64()]
	}
	return string(b)
}

func (c *adminCLI) entry(v string, given bool) error {
	switch {
	case v == "off":
		v = ""
	case v == "" && given:
		v = "/" + randomString(10, "abcdefghijkmnpqrstuvwxyz23456789")
	case v == "":
		return errors.New("要写新的安全入口，或者 off / give the new entry, or off")
	}
	if v != "" && !strings.HasPrefix(v, "/") {
		v = "/" + v
	}
	if v != "" && (!entryRe.MatchString(v) || strings.HasPrefix(v, "/api") || strings.HasPrefix(v, "/assets")) {
		return errors.New("安全入口是 / 开头的 6–32 个字母、数字、- 或 _ / the entry is / followed by 6-32 letters, digits, - or _")
	}
	if err := c.setSetting("access.entry", v); err != nil {
		return err
	}
	if v == "" {
		say("✓ 安全入口已关闭。", "✓ The security entry is off.")
	} else {
		sayf("✓ 安全入口已改为 %s", "✓ The security entry is now %s", v)
	}
	c.restart()
	return c.info()
}

// super picks the administrator to change: the only one, or the one named.
func (c *adminCLI) super() (int64, string, error) {
	rows, err := c.db.Query(`SELECT id, username FROM users WHERE role=? ORDER BY id`, RoleSuper)
	if err != nil {
		return 0, "", err
	}
	type u struct {
		id   int64
		name string
	}
	var list []u
	for rows.Next() {
		var x u
		rows.Scan(&x.id, &x.name)
		list = append(list, x)
	}
	rows.Close()
	switch len(list) {
	case 0:
		return 0, "", errors.New("还没有管理员，请先打开初始化链接 / no administrator yet: open the setup link first (berth admin info)")
	case 1:
		return list[0].id, list[0].name, nil
	}
	for i, x := range list {
		fmt.Printf(" %d) %s\n", i+1, x.name)
	}
	var k int
	fmt.Sscan(c.ask("改哪一个", "Which one"), &k)
	if k < 1 || k > len(list) {
		return 0, "", errors.New("没有这个选项 / no such option")
	}
	return list[k-1].id, list[k-1].name, nil
}

func (c *adminCLI) username(v string) error {
	id, old, err := c.super()
	if err != nil {
		return err
	}
	if v == "" {
		v = c.ask(fmt.Sprintf("新的用户名（现在是 %s）", old), fmt.Sprintf("New username (now %s)", old))
	}
	if !usernameRe.MatchString(v) {
		return errors.New("用户名是 3–32 个字母、数字、_ . - / 3-32 letters, digits, _ . -")
	}
	var n int
	c.db.QueryRow(`SELECT COUNT(*) FROM users WHERE username=? AND id<>?`, v, id).Scan(&n)
	if n > 0 {
		return errors.New("这个用户名已经有人用了 / that username is taken")
	}
	if _, err := c.db.Exec(`UPDATE users SET username=? WHERE id=?`, v, id); err != nil {
		return err
	}
	sayf("✓ 用户名 %s 已改为 %s", "✓ Username %s is now %s", old, v)
	return nil
}

func (c *adminCLI) password(random bool) error {
	id, name, err := c.super()
	if err != nil {
		return err
	}
	var pw string
	if random {
		pw = randomString(16, "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789")
	} else {
		pw = c.secret(fmt.Sprintf("%s 的新密码（至少 10 位）", name), fmt.Sprintf("New password for %s (10+ characters)", name))
		if again := c.secret("再输一次", "Again"); again != pw {
			return errors.New("两次输入不一样 / the two do not match")
		}
	}
	if err := validPassword(pw); err != nil {
		return errors.New("密码至少 10 位，且不能全是同一个字符 / at least 10 characters, not all the same")
	}
	h, err := hashPassword(pw)
	if err != nil {
		return err
	}
	// a passkey-only account could not use the new password: let the password sign in again
	if _, err := c.db.Exec(`UPDATE users SET password_hash=?, disabled=0, login_mode=CASE WHEN login_mode='passkey_only' THEN 'password' ELSE login_mode END WHERE id=?`, h, id); err != nil {
		return err
	}
	c.db.Exec(`DELETE FROM sessions WHERE user_id=?`, id) // signed out everywhere
	if random {
		sayf("✓ %s 的新密码：%s", "✓ New password for %s: %s", name, pw)
		say("请登录后在「账号」里改成自己记得住的密码。", "Sign in and change it under Account to one you will remember.")
	} else {
		sayf("✓ %s 的密码已更新，所有已登录的地方都已退出。", "✓ %s's password is set; every signed-in session was signed out.", name)
	}
	c.restart() // also clears the sign-in lockout
	return nil
}

func (c *adminCLI) secret(zh, en string) string {
	fmt.Printf("%s / %s: ", zh, en)
	exec.Command("stty", "-F", "/dev/tty", "-echo").Run()
	s, _ := c.in.ReadString('\n')
	exec.Command("stty", "-F", "/dev/tty", "echo").Run()
	fmt.Println()
	return strings.TrimSpace(s)
}

// public turns public access on or off: the setting, a security entry (made up when
// missing), and the panel port in firewalld or ufw. Cloud security groups are outside
// this machine, so it says so.
func (c *adminCLI) public(on bool) error {
	_, port, _ := net.SplitHostPort(c.cfg.Listen)
	if port == "" {
		port = "7090"
	}
	if on && c.setting("access.entry") == "" {
		e := "/" + randomString(10, "abcdefghijkmnpqrstuvwxyz23456789")
		if err := c.setSetting("access.entry", e); err != nil {
			return err
		}
		sayf("已生成安全入口 %s（不带它访问，面板只回 404）", "Made a security entry %s (without it the panel answers 404)", e)
	}
	v := ""
	if on {
		v = "1"
	}
	if err := c.setSetting("access.public", v); err != nil {
		return err
	}
	c.openPort(port, on)
	if on {
		say("✓ 公网访问已开启。用的是云服务器的话，还要在云服务商控制台的「安全组 / 防火墙」里放行 TCP "+port+"。",
			"✓ Public access is on. On a cloud server, also allow TCP "+port+" in the provider's security group / firewall.")
		say("  强烈建议登录后马上开启二步验证。", "  Turn on two-step sign-in right after you sign in.")
	} else {
		say("✓ 公网访问已关闭，只有内网和 Tailscale 能打开面板。", "✓ Public access is off: private networks and Tailscale only.")
	}
	var n int
	c.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if n == 0 {
		// the restarted panel writes a fresh setup link; drop the old one so nobody waits on it
		os.Remove(filepath.Join(c.cfg.DataDir, "setup-url"))
	}
	c.restart()
	return c.info()
}

func (c *adminCLI) openPort(port string, on bool) {
	if exec.Command("firewall-cmd", "--state").Run() == nil {
		op := "--remove-port=" + port + "/tcp"
		if on {
			op = "--add-port=" + port + "/tcp"
		}
		exec.Command("firewall-cmd", op).Run()
		if err := exec.Command("firewall-cmd", "--permanent", op).Run(); err == nil {
			c.portSaid("firewalld", port, on)
		}
		return
	}
	if out, err := exec.Command("ufw", "status").Output(); err == nil && strings.Contains(string(out), "Status: active") {
		args := []string{"allow", port + "/tcp"}
		if !on {
			args = []string{"delete", "allow", port + "/tcp"}
		}
		if exec.Command("ufw", args...).Run() == nil {
			c.portSaid("ufw", port, on)
		}
	}
}

func (c *adminCLI) portSaid(fw, port string, on bool) {
	if on {
		sayf(fw+"：已放行 %s/tcp", fw+": opened %s/tcp", port)
	} else {
		sayf(fw+"：已关闭 %s/tcp", fw+": closed %s/tcp", port)
	}
}
