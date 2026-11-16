// Berth plugin example: runs as root and shows who it is.
package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		id, _ := exec.Command("id").Output()
		kernel, _ := os.ReadFile("/proc/sys/kernel/osrelease")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<h1>whoami-root</h1><p id=\"who\">%s</p><p>kernel %s</p>", strings.TrimSpace(string(id)), strings.TrimSpace(string(kernel)))
	})
	http.ListenAndServe("127.0.0.1:"+os.Getenv("PORT"), nil)
}
