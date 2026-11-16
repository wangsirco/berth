package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/cli"
	"github.com/wangsirco/berth/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "agent" {
		if err := runAgent(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "berth agent:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "update-finish" {
		if err := runUpdateFinish(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "berth update-finish:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 2 && os.Args[1] == "sync-serve" { // forced command of a file-sync key (see internal/agent/syncfiles.go)
		args, err := agent.SyncServeArgs(os.Args[2], os.Getenv("SSH_ORIGINAL_COMMAND"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "berth sync-serve:", err)
			os.Exit(1)
		}
		bin, err := exec.LookPath("rsync")
		if err != nil {
			fmt.Fprintln(os.Stderr, "berth sync-serve: rsync is not installed")
			os.Exit(1)
		}
		err = syscall.Exec(bin, args, os.Environ())
		fmt.Fprintln(os.Stderr, "berth sync-serve:", err)
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("berth", version.Version)
		return
	}
	os.Exit(cli.Main(os.Args[1:])) // Task 13
}
