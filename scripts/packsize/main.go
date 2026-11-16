// packsize: how big `berth push` would upload for a directory (dev tool).
package main

import (
	"fmt"
	"os"

	"github.com/wangsirco/berth/internal/cli"
)

func main() {
	b, n, err := cli.Pack(os.Args[1])
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	fmt.Printf("%d files, %.1f MB\n", n, float64(len(b))/1e6)
	if len(os.Args) > 2 { // packsize DIR OUT.tar.gz writes the archive too
		if err := os.WriteFile(os.Args[2], b, 0o600); err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
	}
}
