// Command psdkit-web-build builds the browser executable and copies its matching Go runtime.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	out := flag.String("out", "web/demo/public/wasm", "output directory")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(directory string) error {
	toolchainJSON, err := exec.Command("go", "env", "-json", "GOROOT", "GOVERSION").Output()
	if err != nil {
		return fmt.Errorf("locate Go toolchain: %w", err)
	}
	var toolchain struct {
		GOROOT    string
		GOVERSION string
	}
	if err := json.Unmarshal(toolchainJSON, &toolchain); err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	docs := exec.Command("go", "run", "./cmd/psdkit-web-docs")
	docs.Stdout, docs.Stderr = os.Stdout, os.Stderr
	if err := docs.Run(); err != nil {
		return err
	}
	// Playground 示例文档与 Wasm 同属构建产物，放在 wasm 目录旁的 samples/。
	samples := exec.Command("go", "run", "./cmd/psdkit-web-samples", "-out", filepath.Join(filepath.Dir(directory), "samples"))
	samples.Stdout, samples.Stderr = os.Stdout, os.Stderr
	if err := samples.Run(); err != nil {
		return err
	}
	build := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o",
		filepath.Join(directory, "psdkit.wasm"), "./cmd/psdkit-wasm")
	build.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return err
	}
	for _, item := range [][2]string{
		{filepath.Join(toolchain.GOROOT, "lib", "wasm", "wasm_exec.js"), "wasm_exec.js"},
		{filepath.Join(toolchain.GOROOT, "LICENSE"), "LICENSE-Go.txt"},
		{"web/sdk/worker.js", "worker.js"},
	} {
		data, err := os.ReadFile(item[0])
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(directory, item[1]), data, 0644); err != nil {
			return err
		}
	}
	hashes := map[string]string{}
	for _, name := range []string{"psdkit.wasm", "wasm_exec.js", "worker.js"} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		hashes[name] = hex.EncodeToString(digest[:])
	}
	manifest, err := json.Marshal(map[string]any{"goVersion": toolchain.GOVERSION, "protocol": 1,
		"target": "js/wasm", "sha256": hashes}, jsontext.WithIndent("  "), json.Deterministic(true))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), append(manifest, '\n'), 0644); err != nil {
		return err
	}
	fmt.Println("Built browser runtime in", directory)
	return nil
}
