package panel

import (
	"strings"
	"testing"
)

func TestCloudInit(t *testing.T) {
	withKeys := cloudInit("debian", []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyExampleKeyExampleKeyExample me@mac"}, "Pw123")
	for _, want := range []string{"#cloud-config\n", "packages: [openssh-server]", "ssh_pwauth: false", `- "ssh-ed25519 AAAAC3`, `password: "Pw123"`, "systemctl enable --now ssh"} {
		if !strings.Contains(withKeys, want) {
			t.Errorf("missing %q in\n%s", want, withKeys)
		}
	}
	if strings.Contains(withKeys, "PermitRootLogin yes") {
		t.Error("password login opened although keys are set")
	}
	noKeys := cloudInit("alpine", nil, "Pw123")
	for _, want := range []string{"packages: [openssh]", "ssh_pwauth: true", "PermitRootLogin yes", "rc-update add sshd"} {
		if !strings.Contains(noKeys, want) {
			t.Errorf("missing %q in\n%s", want, noKeys)
		}
	}
	// a key with a quote cannot break out of the YAML string
	odd := cloudInit("debian", []string{`ssh-ed25519 AAAA" injected: true`}, "")
	if strings.Contains(odd, "\ninjected: true") {
		t.Fatal("YAML injection")
	}
}

func TestSSHKeyFormat(t *testing.T) {
	good := []string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyExampleKeyExampleKeyExample me@mac",
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDExampleKeyExampleKeyExampleKeyExample",
		"ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYExample= laptop",
	}
	for _, k := range good {
		if !sshKeyRe.MatchString(k) {
			t.Errorf("rejected %q", k)
		}
	}
	for _, k := range []string{"", "not a key", "ssh-ed25519 short", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyExampleKeyExampleKeyExample x\nssh-rsa evil"} {
		if sshKeyRe.MatchString(k) {
			t.Errorf("accepted %q", k)
		}
	}
	if p := randomPassword(16); len(p) != 16 || strings.ContainsAny(p, "0O1lI") {
		t.Errorf("password %q", p)
	}
}
