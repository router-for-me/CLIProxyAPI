package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/mod/semver"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: validate_tag <tag>")
		os.Exit(2)
	}
	tag := strings.TrimSpace(os.Args[1])
	if tag == "" {
		fmt.Fprintln(os.Stderr, "tag is empty")
		os.Exit(2)
	}
	if !strings.HasPrefix(tag, "v") {
		fmt.Fprintf(os.Stderr, "tag must start with 'v': %s\n", tag)
		os.Exit(2)
	}
	if !semver.IsValid(tag) {
		fmt.Fprintf(os.Stderr, "tag is not valid semver: %s\n", tag)
		os.Exit(2)
	}
}
