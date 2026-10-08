//go:build js

// The browser build: the same pipeline as the server, in a Web Worker
// (wasm/worker.js). The worker puts the model files in an in-memory file
// system (wasm/memfs.js) under /model, then runs this program, which
// loads the model, frees those files and exposes caviarder to the worker.
// Documents go through the same in-memory file system, since go-anon's
// readers only read files: nothing leaves the browser tab.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall/js"
	"time"

	"github.com/bornholm/go-anon/pkg/anonymizer"
	"github.com/bornholm/indecis"
)

const modelDir = "/model"

func main() {
	self := js.Global()
	start := time.Now()
	m, err := indecis.Load(modelDir)
	if err != nil {
		self.Call("onGoError", "chargement du modèle : "+err.Error())
		return
	}
	// The model is now in Go's memory: the copies in the file system
	// would only double the tab's memory.
	os.RemoveAll(modelDir)
	debug.FreeOSMemory()

	self.Set("caviarder", js.FuncOf(func(_ js.Value, args []js.Value) any {
		name, data, bias, strategy := args[0].String(), args[1], args[2].Float(), args[3].String()
		return promise(func() (any, error) { return redactBytes(m, name, data, bias, strategy) })
	}))
	h := indecis.DetectHardware()
	self.Call("onGoReady", map[string]any{
		"seconds":      time.Since(start).Seconds(),
		"simd":         h.SIMD,
		"simdEmulated": h.SIMDEmulated,
		"vectorBits":   h.VectorBits,
	})
	select {}
}

// redactBytes redacts a document given as a Uint8Array and returns the
// page's response as JSON with the document produced.
func redactBytes(m *indecis.Model, name string, data js.Value, bias float64, strategy string) (any, error) {
	ext := strings.ToLower(filepath.Ext(name))
	open, ok := walkers[ext]
	if !ok {
		return nil, fmt.Errorf("format non pris en charge : Word (.docx) ou OpenDocument (.odt)")
	}
	strat := anonymizer.Redact
	if strategy == "tag" {
		strat = anonymizer.TagReplace
	}
	dir, err := os.MkdirTemp("", "caviardage-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in, out := filepath.Join(dir, "in"+ext), filepath.Join(dir, "out"+ext)
	b := make([]byte, data.Length())
	js.CopyBytesToGo(b, data)
	err = os.WriteFile(in, b, 0o600)
	clear(b)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	res, err := process(m, open, in, out, bias, strat)
	if err != nil {
		return nil, fmt.Errorf("le document n'a pas pu être traité : %w", err)
	}
	res.Seconds = time.Since(start).Seconds()
	res.Name = strings.TrimSuffix(name, filepath.Ext(name)) + "_caviarde" + ext
	if strat == anonymizer.TagReplace {
		res.Name = strings.TrimSuffix(name, filepath.Ext(name)) + "_pseudonymise" + ext
	}
	produced, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	doc := js.Global().Get("Uint8Array").New(len(produced))
	js.CopyBytesToJS(doc, produced)
	clear(produced)
	body, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	return map[string]any{"response": string(body), "document": doc}, nil
}

// promise runs f outside the JavaScript callback, which must not block,
// and settles a Promise with its result.
func promise(f func() (any, error)) js.Value {
	return js.Global().Get("Promise").New(js.FuncOf(func(_ js.Value, args []js.Value) any {
		resolve, reject := args[0], args[1]
		go func() {
			v, err := f()
			if err != nil {
				reject.Invoke(js.Global().Get("Error").New(err.Error()))
				return
			}
			resolve.Invoke(v)
		}()
		return nil
	}))
}
