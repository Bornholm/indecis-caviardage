// Command indecis-caviardage redacts the personal data of Word and ODT
// documents in a browser page, and lists what it redacted. Everything
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

func main() {
	model := flag.String("model", "../indecis-ner/runs/work-fam", "indecis masking model (mask and sensitive questions)")
	addr := flag.String("addr", "127.0.0.1:8071", "listen address")
	threads := flag.Int("threads", 0, "cores per document (0: all)")
	maxSize := flag.Int64("max-size", 30<<20, "largest document accepted, in bytes")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	start := time.Now()
	m, err := indecis.Load(*model, indecis.WithInt8(), indecis.WithThreads(*threads))
	if err != nil {
		log.Error("loading the model", "error", err)
		os.Exit(1)
	}
	debug.FreeOSMemory()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	log.Info("model loaded", "dir", *model, "duration", time.Since(start).Round(time.Millisecond), "heap MiB", ms.HeapAlloc>>20)

	a := newApp(m, log, *maxSize)
	static, _ := fs.Sub(web, "web")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(static)))
	mux.HandleFunc("POST /api/redact", a.redact)
	mux.HandleFunc("GET /api/download/{token}", a.download)
	log.Info("open in a browser", "url", "http://"+*addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Error("server", "error", err)
		os.Exit(1)
	}
}
