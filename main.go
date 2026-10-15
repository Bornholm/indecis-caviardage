//go:build !js

// Command indecis-caviardage redacts the personal data of Word, ODT and
// CSV documents in a browser page, and lists what it redacted. Everything
// runs on this machine, on CPU: an indecis model finds the passages to
// mask, direct and indirect identifiers and special categories of data
// (github.com/bornholm/indecis-ner), and go-anon rewrites the document,
// checks the result and strips hidden surfaces (metadata, comments,
// revisions).
//
//	indecis-caviardage -model ../indecis-ner/runs/work-fam
//
// then open http://127.0.0.1:8071.
package main

import (
	"embed"
	"flag"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/bornholm/indecis"
)

//go:embed web
var web embed.FS

// demo holds the fictitious documents the page offers to try.
//
//go:embed demo/compte-rendu.docx demo/compte-rendu.odt demo/clients.csv
var demo embed.FS

func main() {
	model := flag.String("model", "../indecis-ner/runs/work-fam", "indecis masking model (mask and sensitive questions)")
	addr := flag.String("addr", "127.0.0.1:8071", "listen address")
	threads := flag.Int("threads", 0, "cores used by the computations (0: all)")
	workers := flag.Int("workers", 2, "documents processed at the same time, each with its own instance of the model")
	queue := flag.Int("queue", 8, "documents waiting for an instance of the model; beyond, requests are refused")
	maxSize := flag.Int64("max-size", 200<<20, "largest document accepted, in bytes")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	start := time.Now()
	models := make([]*indecis.Model, max(*workers, 1))
	for i := range models {
		m, err := indecis.Load(*model, indecis.WithInt8(), indecis.WithThreads(*threads))
		if err != nil {
			log.Error("loading the model", "error", err)
			os.Exit(1)
		}
		models[i] = m
	}
	debug.FreeOSMemory()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	log.Info("model loaded", "dir", *model, "instances", len(models), "duration", time.Since(start).Round(time.Millisecond), "heap MiB", ms.HeapAlloc>>20)

	a, err := newApp(models, *queue, log, *maxSize)
	if err != nil {
		log.Error("results directory", "error", err)
		os.Exit(1)
	}
	static, _ := fs.Sub(web, "web")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(static)))
	mux.Handle("GET /demo/", http.FileServer(http.FS(demo)))
	mux.HandleFunc("POST /api/redact", a.redact)
	mux.HandleFunc("GET /api/download/{token}", a.download)
	log.Info("open in a browser", "url", "http://"+*addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Error("server", "error", err)
		os.Exit(1)
	}
}
