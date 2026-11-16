// hashes prints the web and core fingerprints used in release manifests and ldflags.
package main

import (
	"fmt"
	"os"

	"github.com/wangsirco/berth/internal/release"
)

func main() {
	web, err := release.WebHash(os.DirFS("web/dist"))
	if err != nil {
		panic(err)
	}
	core, err := release.CoreHash(".")
	if err != nil {
		panic(err)
	}
	fmt.Println(web, core)
}
