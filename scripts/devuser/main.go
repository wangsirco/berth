//go:build devtools

// devuser: add or remove a temporary super admin on a dev panel (end-to-end tests).
//
//	devuser add NAME   (password on stdin)
//	devuser del NAME
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/wangsirco/berth/internal/panel"
	"github.com/wangsirco/berth/internal/store"
)

func main() {
	if len(os.Args) != 3 || (os.Args[1] != "add" && os.Args[1] != "del") {
		fmt.Fprintln(os.Stderr, "usage: devuser add|del NAME")
		os.Exit(2)
	}
	db, err := store.Open("/var/lib/berth/berth.db")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pw := ""
	if os.Args[1] == "add" {
		pw, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		pw = strings.TrimSpace(pw)
	}
	if err := panel.DevUser(db, os.Args[2], pw, os.Args[1] == "del"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("ok")
}
