package incus

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestAgentGuard(t *testing.T) {
	wf, run := AgentGuard()
	doc := "#cloud-config\n" + wf + "runcmd:\n" + run
	var m struct {
		WriteFiles []struct{ Path, Content string } `yaml:"write_files"`
		Runcmd     [][]string                       `yaml:"runcmd"`
	}
	if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatal(err, doc)
	}
	if len(m.WriteFiles) != 2 || m.WriteFiles[0].Content != AgentPolicy {
		t.Fatalf("policy file: %+v", m.WriteFiles)
	}
	unit := m.WriteFiles[1].Content
	for _, want := range []string{"Before=incus-agent.service", "ConditionPathExists=/sys/fs/selinux/enforce", "grep -qx berth-incus-agent || semodule -i /etc/berth/berth-incus-agent.cil", "WantedBy=multi-user.target"} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	if len(m.Runcmd) != 1 || !strings.Contains(m.Runcmd[0][2], "systemctl enable berth-incus-agent-selinux.service") {
		t.Fatalf("runcmd: %v", m.Runcmd)
	}
	// the command people paste writes the same module
	cmd := AgentFixCommand()
	if !strings.Contains(cmd, `(allow init_t self (vsock_socket (listen accept)))\n`) || strings.Contains(cmd, "\n") {
		t.Fatal(cmd)
	}
}
