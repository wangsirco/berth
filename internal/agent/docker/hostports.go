package docker

import (
	"net/http"
	"os"

	"github.com/wangsirco/berth/internal/agent"
)

// HostListening lists TCP ports listening in the host's own network namespace.
func HostListening() []Listen {
	var out []Listen
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		if r, err := os.Open(f); err == nil {
			out = append(out, parseProcNet(r, "tcp")...)
			r.Close()
		}
	}
	return out
}

func registerHostPorts(s *agent.Server, c *Client) {
	s.Handle("GET /v1/net/listening", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		ports := map[int]bool{}
		for _, l := range HostListening() {
			ports[l.Port] = true
		}
		// published ports, also when Docker forwards them without a userland proxy
		if cs, err := c.Containers(r.Context()); err == nil {
			for _, ct := range cs {
				for _, p := range ct.Ports {
					if p.Public > 0 {
						ports[p.Public] = true
					}
				}
			}
		}
		out := make([]int, 0, len(ports))
		for p := range ports {
			out = append(out, p)
		}
		agent.WriteJSON(w, 200, out)
	})
}
