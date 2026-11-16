package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os/exec"
	"time"

	"github.com/wangsirco/berth/internal/agent"
)

// runUpdateFinish is started by systemd-run after the agent installed a release: restart
// both services, wait for the new panel to report the new version, else roll back.
func runUpdateFinish(args []string) error {
	fs := flag.NewFlagSet("update-finish", flag.ContinueOnError)
	rb := fs.String("rollback", "", "rollback directory")
	from := fs.String("from", "", "old version")
	to := fs.String("to", "", "new version")
	port := fs.String("port", "7090", "panel port")
	wait := fs.Duration("wait", 60*time.Second, "how long the new panel has to come up")
	agentOnly := fs.Bool("agent", false, "a remote host's agent: restart only it and wait for it to reconnect")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *agentOnly {
		return agentUpdateFinish(*rb, *from, *to, *wait)
	}
	restart := func() {
		_ = exec.Command("systemctl", "daemon-reload").Run()
		_ = exec.Command("systemctl", "restart", "berth-agent", "berth-panel").Run()
	}
	restart()
	if healthy(*port, *to, *wait) {
		agent.WriteResult(agent.UpdateResult{From: *from, To: *to, OK: true})
		return nil
	}
	agent.Restore(*rb, true)
	restart()
	ok := healthy(*port, *from, *wait)
	agent.WriteResult(agent.UpdateResult{From: *from, To: *to, RolledBack: true, Detail: fmt.Sprintf("new panel did not report %s within %s; restored %s (healthy after rollback: %v)", *to, *wait, *from, ok)})
	return nil
}

func healthy(port, want string, wait time.Duration) bool {
	hc := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} // local loopback self-check only
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		resp, err := hc.Get("https://127.0.0.1:" + port + "/api/v1/meta")
		if err == nil {
			var m struct{ Version string }
			_ = json.NewDecoder(resp.Body).Decode(&m)
			resp.Body.Close()
			if m.Version == want {
				return true
			}
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// agentUpdateFinish restarts a remote agent on its new binary; if that version does not
// reconnect to the panel in time, the old binary goes back.
func agentUpdateFinish(rb, from, to string, wait time.Duration) error {
	start := time.Now()
	_ = exec.Command("systemctl", "restart", "berth-agent").Run()
	for time.Since(start) < wait {
		if agent.ConnectedSince(to, start) {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	agent.Restore(rb, false)
	_ = exec.Command("systemctl", "restart", "berth-agent").Run()
	return fmt.Errorf("agent %s did not reconnect within %s; restored %s", to, wait, from)
}
