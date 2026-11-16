package system

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Network changes go through a NetworkManager checkpoint: NetworkManager itself puts the
// old settings back after the timeout unless the panel confirms, so a wrong address or a
// lost route cannot keep anyone out for good (even if the agent stops meanwhile).

const checkpointFile = "/var/lib/berth/net-checkpoint"

type NICSpec struct {
	Method4  string   `json:"method4"`  // auto | manual
	Addrs4   []string `json:"addrs4"`   // manual: "ip/prefix"
	Gateway4 string   `json:"gateway4"` // manual
	DNS4     []string `json:"dns4"`     // extra or replacement resolvers
	OwnDNS   bool     `json:"own_dns"`  // ignore the resolvers DHCP hands out
	MTU      int      `json:"mtu"`      // 0 = leave
	Up       *bool    `json:"up"`       // connect / disconnect the card
}

var ifnameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,15}$`)

func (s NICSpec) validate() error {
	if s.Method4 != "" && s.Method4 != "auto" && s.Method4 != "manual" {
		return errors.New("method must be auto or manual")
	}
	if s.Method4 == "manual" {
		if len(s.Addrs4) == 0 {
			return errors.New("manual needs an address")
		}
		for _, a := range s.Addrs4 {
			ip, _, err := net.ParseCIDR(a)
			if err != nil || ip.To4() == nil {
				return fmt.Errorf("bad address %q (use 192.168.1.10/24)", a)
			}
		}
		if s.Gateway4 != "" && net.ParseIP(s.Gateway4).To4() == nil {
			return fmt.Errorf("bad gateway %q", s.Gateway4)
		}
	}
	for _, d := range s.DNS4 {
		if net.ParseIP(d) == nil {
			return fmt.Errorf("bad DNS server %q", d)
		}
	}
	if s.MTU != 0 && (s.MTU < 576 || s.MTU > 9216) {
		return errors.New("MTU must be between 576 and 9216")
	}
	return nil
}

func busctl(ctx context.Context, method string, args ...string) (string, error) {
	a := append([]string{"call", "org.freedesktop.NetworkManager", "/org/freedesktop/NetworkManager", "org.freedesktop.NetworkManager", method}, args...)
	return run(ctx, "busctl", a...)
}

// checkpoint asks NetworkManager to snapshot every device, rolling back after `secs`.
func checkpoint(ctx context.Context, secs int) (string, error) {
	out, err := busctl(ctx, "CheckpointCreate", "aouu", "0", strconv.Itoa(secs), "1") // 1 = replace older checkpoints
	if err != nil {
		return "", errors.New("NetworkManager checkpoint: " + out)
	}
	m := regexp.MustCompile(`"(/org/freedesktop/NetworkManager/Checkpoint/\d+)"`).FindStringSubmatch(out)
	if m == nil {
		return "", errors.New("NetworkManager gave no checkpoint: " + out)
	}
	os.MkdirAll("/var/lib/berth", 0o755)
	os.WriteFile(checkpointFile, []byte(m[1]+"\n"+strconv.FormatInt(time.Now().Add(time.Duration(secs)*time.Second).Unix(), 10)+"\n"), 0o600)
	return m[1], nil
}

// Pending: the checkpoint waiting for confirmation, and when it rolls back.
func Pending() (string, int64) {
	b, err := os.ReadFile(checkpointFile)
	if err != nil {
		return "", 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return "", 0
	}
	at, _ := strconv.ParseInt(f[1], 10, 64)
	if at < time.Now().Unix() {
		os.Remove(checkpointFile) // NetworkManager has rolled it back by now
		return "", 0
	}
	return f[0], at
}

func ConfirmNet(ctx context.Context) error {
	cp, _ := Pending()
	if cp == "" {
		return errors.New("nothing waits for confirmation (it may have rolled back already)")
	}
	os.Remove(checkpointFile)
	if out, err := busctl(ctx, "CheckpointDestroy", "o", cp); err != nil {
		return errors.New(out)
	}
	return nil
}

func RollbackNet(ctx context.Context) error {
	cp, _ := Pending()
	if cp == "" {
		return nil
	}
	os.Remove(checkpointFile)
	if out, err := busctl(ctx, "CheckpointRollback", "o", cp); err != nil {
		return errors.New(out)
	}
	return nil
}

// ApplyNIC changes one card's NetworkManager profile under a checkpoint; the caller
// confirms within `secs` or it all goes back.
func ApplyNIC(ctx context.Context, dev string, s NICSpec, secs int, log func(string)) error {
	if !ifnameRe.MatchString(dev) {
		return errors.New("bad interface name")
	}
	if err := s.validate(); err != nil {
		return err
	}
	conn, ok := nmDevices(ctx)[dev]
	if !ok {
		return errors.New(dev + " is not managed by NetworkManager here")
	}
	c := nmConn(ctx, conn)
	if c == nil {
		return errors.New("cannot read the profile of " + dev)
	}
	if secs < 30 || secs > 600 {
		secs = 60
	}
	cp, err := checkpoint(ctx, secs)
	if err != nil {
		return err
	}
	log(fmt.Sprintf("checkpoint %s: rolls back in %d s unless confirmed", cp, secs))
	if s.Up != nil {
		verb := "connect"
		if !*s.Up {
			verb = "disconnect"
		}
		out, err := run(ctx, "nmcli", "device", verb, dev)
		log(out)
		if err != nil {
			return errors.New(out)
		}
		return nil
	}
	args := []string{"connection", "modify", c.UUID}
	switch s.Method4 {
	case "auto":
		args = append(args, "ipv4.method", "auto", "ipv4.addresses", "", "ipv4.gateway", "")
	case "manual":
		args = append(args, "ipv4.method", "manual", "ipv4.addresses", strings.Join(s.Addrs4, ","), "ipv4.gateway", s.Gateway4)
	}
	if s.DNS4 != nil {
		args = append(args, "ipv4.dns", strings.Join(s.DNS4, " "))
		ignore := "no"
		if s.OwnDNS && len(s.DNS4) > 0 {
			ignore = "yes"
		}
		args = append(args, "ipv4.ignore-auto-dns", ignore)
	}
	if s.MTU > 0 {
		args = append(args, "802-3-ethernet.mtu", strconv.Itoa(s.MTU))
	}
	if len(args) > 3 {
		log("$ nmcli " + strings.Join(args, " "))
		if out, err := run(ctx, "nmcli", args...); err != nil {
			RollbackNet(ctx)
			return errors.New(out)
		}
	}
	// bring the profile up again so the new settings take effect
	up, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out, err := run(up, "nmcli", "--wait", "40", "connection", "up", c.UUID)
	log(out)
	if err != nil {
		RollbackNet(ctx)
		return errors.New("the new settings did not come up, rolled back: " + out)
	}
	return nil
}
