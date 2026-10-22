//go:build !js || !wasm

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "Build the editor with GOOS=js GOARCH=wasm.")
	os.Exit(2)
}
