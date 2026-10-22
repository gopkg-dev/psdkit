//go:build !js || !wasm

package main

import "fmt"

func main() {
	fmt.Println("Build this entry with GOOS=js GOARCH=wasm, or run go run ./cmd/psdkit-web-build.")
}
