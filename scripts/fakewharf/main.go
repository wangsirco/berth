// fakewharf runs the test Wharf for end-to-end checks next to a dev panel. Development
// tool only; not shipped.
//
//	fakewharf -listen 127.0.0.1:41443      (any pairing code is accepted)
//	GET /debug/state on the same address returns what Berth did to it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/wangsirco/berth/internal/panel/wharfapi/wharftest"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:41443", "address")
	state := flag.String("state", "127.0.0.1:41444", "plain-HTTP address for /debug/state")
	proxy := flag.Bool("proxy-front", false, "443 is taken by the proxy front (SNI registration possible)")
	flag.Parse()
	f, err := wharftest.NewOn(*addr)
	if err != nil {
		log.Fatal(err)
	}
	f.Code, f.ProxyFront = "*", *proxy
	fmt.Println(f.URL, f.FP)
	http.HandleFunc("/debug/state", func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(f.State()) })
	log.Fatal(http.ListenAndServe(*state, nil))
}
