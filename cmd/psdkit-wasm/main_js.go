//go:build js && wasm

// Command psdkit-wasm installs the local worker bridge without exposing filesystem paths.
package main

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"runtime/debug"
	"syscall/js"

	"github.com/gopkg-dev/psdkit/psdweb"
)

var callbacks []js.Func

func main() {
	engine, err := psdweb.New(psdweb.Config{})
	if err != nil {
		panic(err)
	}
	debug.SetMemoryLimit(384 << 20)
	call := js.FuncOf(func(this js.Value, arguments []js.Value) any {
		constructor := js.FuncOf(func(this js.Value, promise []js.Value) any {
			resolve, reject := promise[0], promise[1]
			if len(arguments) == 0 || arguments[0].Type() != js.TypeString {
				reject.Invoke("JSON request is required")
				return nil
			}
			var request psdweb.Request
			if err := json.Unmarshal([]byte(arguments[0].String()), &request); err != nil {
				reject.Invoke(err.Error())
				return nil
			}
			if request.TimeoutMS == 0 {
				request.TimeoutMS = 60_000
			}
			var input []byte
			if len(arguments) > 1 && !arguments[1].IsNull() && !arguments[1].IsUndefined() {
				length := arguments[1].Get("byteLength").Int()
				if length < 0 || length > 256<<20 {
					reject.Invoke("Input exceeds 256 MiB")
					return nil
				}
				input = make([]byte, length)
				if count := js.CopyBytesToGo(input, arguments[1]); count != length {
					reject.Invoke("Incomplete binary input")
					return nil
				}
			}
			go func() {
				response := engine.Execute(context.Background(), request, input)
				metadata, err := json.Marshal(response)
				if err != nil {
					reject.Invoke(err.Error())
					return
				}
				result := js.Global().Get("Object").New()
				result.Set("json", string(metadata))
				payloads := js.Global().Get("Array").New(len(response.Binaries))
				for index, binary := range response.Binaries {
					data := js.Global().Get("Uint8Array").New(len(binary.Data))
					js.CopyBytesToJS(data, binary.Data)
					payloads.SetIndex(index, data)
				}
				result.Set("payloads", payloads)
				resolve.Invoke(result)
			}()
			return nil
		})
		promise := js.Global().Get("Promise").New(constructor)
		constructor.Release()
		return promise
	})
	callbacks = append(callbacks, call)
	api := js.Global().Get("Object").New()
	api.Set("call", call)
	api.Set("version", "psdkit-worker-v1")
	js.Global().Set("psdkitWasm", api)
	if ready := js.Global().Get("__psdkitReady"); ready.Type() == js.TypeFunction {
		ready.Invoke("psdkit-worker-v1")
	}
	fmt.Println("psdkit WebAssembly worker ready")
	select {}
}
