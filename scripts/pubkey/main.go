// pubkey reads a base64 ed25519 seed on stdin and prints the base64 public key.
// With -new it prints a fresh seed instead (for a development key).
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	gen := flag.Bool("new", false, "print a new seed")
	flag.Parse()
	if *gen {
		seed := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			panic(err)
		}
		fmt.Println(base64.StdEncoding.EncodeToString(seed))
		return
	}
	in, _ := io.ReadAll(os.Stdin)
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(in)))
	if err != nil || len(seed) != ed25519.SeedSize {
		fmt.Fprintln(os.Stderr, "pubkey: need a base64 32-byte seed on stdin")
		os.Exit(1)
	}
	fmt.Println(base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)))
}
