package main

import (
	"net/http"
	"os"
	"os/user"
	"strings"
)

func tryw(p string) string {
	if os.WriteFile(p, []byte("x"), 0o644) != nil {
		return "denied"
	}
	return "ok"
}

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		b, _ := os.ReadFile("VERSION")
		w.Write([]byte(strings.TrimSpace(string(b))))
	})
	http.HandleFunc("/whoami", func(w http.ResponseWriter, r *http.Request) {
		u, _ := user.Current()
		w.Write([]byte(u.Username))
	})
	http.HandleFunc("/w-code", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tryw("main.go"))) })
	http.HandleFunc("/w-data", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tryw("data/probe.txt"))) })
	http.ListenAndServe(":"+os.Getenv("PORT"), nil)
}
