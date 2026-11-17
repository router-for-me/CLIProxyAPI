// Command proxx-sign signs a release file (checksums.txt) with the proxx release ed25519 key.
//
//	PROXX_RELEASE_SIGNING_KEY=<base64 32-byte seed> proxx-sign checksums.txt > checksums.txt.sig
//
// The output is the base64 of the 64-byte ed25519 signature over the exact file bytes. With -pub it
// prints the base64 public key for the seed instead (to embed in internal/proxx/selfupdate). With
// -verify <pubkey> it checks a signature read from stdin.
package main

import (
	"crypto/ed25519"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/MoeForge/moego/base64"
)

const seedEnv = "PROXX_RELEASE_SIGNING_KEY"

func main() {
	os.Exit(run())
}

func run() int {
	pubOnly := flag.Bool("pub", false, "print the base64 public key for the seed in "+seedEnv)
	verifyKey := flag.String("verify", "", "verify the base64 signature on stdin against this base64 public key")
	flag.Parse()

	if *verifyKey != "" {
		return verify(*verifyKey, flag.Arg(0))
	}

	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv(seedEnv)))
	if err != nil || len(seed) != ed25519.SeedSize {
		fmt.Fprintf(os.Stderr, "proxx-sign: %s must be the base64 of a %d-byte ed25519 seed\n", seedEnv, ed25519.SeedSize)
		return 2
	}
	priv := ed25519.NewKeyFromSeed(seed)
	if *pubOnly {
		fmt.Println(base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
		return 0
	}
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: proxx-sign <file>")
		return 2
	}
	data, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxx-sign: %v\n", err)
		return 1
	}
	fmt.Println(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data)))
	return 0
}

func verify(pubB64, file string) int {
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubB64))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		fmt.Fprintln(os.Stderr, "proxx-sign: invalid public key")
		return 2
	}
	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxx-sign: %v\n", err)
		return 1
	}
	sigText, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxx-sign: %v\n", err)
		return 1
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigText)))
	if err != nil || !ed25519.Verify(ed25519.PublicKey(pub), data, sig) {
		fmt.Fprintln(os.Stderr, "proxx-sign: signature is INVALID")
		return 1
	}
	fmt.Println("signature OK")
	return 0
}
