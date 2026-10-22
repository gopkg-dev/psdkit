// Command psdkit-web-server serves the static demo or assembled site and optional, loopback-only frozen browser fixtures.
package main

import (
	"encoding/json/v2"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	directory := flag.String("dir", "web/demo/dist", "static demo directory")
	address := flag.String("addr", "127.0.0.1:8194", "loopback listen address")
	fixtures := flag.Bool("fixtures", false, "enable the explicit frozen 66-case regression set")
	output := flag.String("out", "output/wasm-browser-v1", "new verification output directory")
	base := flag.String("base", "/", "URL path prefix for static files, such as /psdkit/ for the assembled Pages site")
	flag.Parse()
	if err := run(*directory, *address, *fixtures, *output, *base); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(directory, address string, fixtures bool, output, base string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("loopback address required")
	}
	if !strings.HasPrefix(base, "/") || !strings.HasSuffix(base, "/") {
		return fmt.Errorf("base must start and end with a slash")
	}
	mux := http.NewServeMux()
	if fixtures {
		suite, err := loadSuite(output)
		if err != nil {
			return err
		}
		mux.HandleFunc("/__fixtures/manifest", suite.manifest)
		mux.HandleFunc("/__fixtures/file/", suite.file)
		mux.HandleFunc("/__fixtures/result", suite.result)
		mux.HandleFunc("/__fixtures/complete", suite.complete)
		mux.HandleFunc("/__fixtures/font", suite.font)
		mux.HandleFunc("/__fixtures/font-result", suite.fontResult)
	}
	files := http.FileServer(http.Dir(directory))
	if base == "/" {
		mux.Handle("/", files)
	} else {
		mux.Handle(base, http.StripPrefix(strings.TrimSuffix(base, "/"), files))
		mux.Handle("/{$}", http.RedirectHandler(base, http.StatusFound))
	}
	server := &http.Server{Addr: address, Handler: http.HandlerFunc(func(
		writer http.ResponseWriter, request *http.Request,
	) {
		writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		origin := request.Header.Get("Origin")
		if origin != "" && origin != "http://"+address && origin != "http://127.0.0.1:5194" {
			http.Error(writer, "origin is not the local demo", http.StatusForbidden)
			return
		}
		if strings.HasSuffix(filepath.Clean(request.URL.Path), ".wasm") {
			writer.Header().Set("Content-Type", "application/wasm")
		}
		mux.ServeHTTP(writer, request)
	})}
	fmt.Println("psdkit browser demo:", "http://"+address+base)
	return server.ListenAndServe()
}

func sendJSON(writer http.ResponseWriter, value any, err error) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		value = map[string]string{"error": err.Error()}
	}
	if err := json.MarshalWrite(writer, value); err != nil {
		fmt.Fprintln(os.Stderr, "write JSON response:", err)
	}
}
