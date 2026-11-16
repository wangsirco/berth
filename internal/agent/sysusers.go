package agent

import (
	"bufio"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// LoginUser is a person's account on the server (UID ≥ 1000 with a real shell).
type LoginUser struct {
	Name  string `json:"name"`
	UID   int    `json:"uid"`
	Home  string `json:"home"`
	Shell string `json:"shell"`
}

func loginUsers(path string) []LoginUser {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	out := []LoginUser{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Split(sc.Text(), ":")
		if len(p) < 7 {
			continue
		}
		uid, _ := strconv.Atoi(p[2])
		if uid < 1000 || uid >= 60000 || strings.HasSuffix(p[6], "nologin") || strings.HasSuffix(p[6], "false") {
			continue
		}
		out = append(out, LoginUser{Name: p[0], UID: uid, Home: p[5], Shell: p[6]})
	}
	return out
}

func registerSystem(s *Server) {
	s.Handle("GET /v1/system/users", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		WriteJSON(w, 200, loginUsers("/etc/passwd"))
	})
}
