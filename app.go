//go:build !js

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bornholm/go-anon/pkg/anonymizer"
	"github.com/bornholm/indecis"
)

// keep is how long a redacted document waits to be downloaded, in memory
// only. It is deleted at its first download, or when it expires.
const keep = 15 * time.Minute

// tempPrefix names the directories a document is processed in: go-anon's
// DOCX and ODT readers only read files.
const tempPrefix = "caviardage-"

// tempRoot is where documents are processed: /dev/shm, in memory, when it
// is available, so that no document is written to a disk whatever /tmp
// is; the system's temporary directory otherwise.
func tempRoot() string {
	if dir, err := os.MkdirTemp("/dev/shm", tempPrefix+"probe-"); err == nil {
		os.Remove(dir)
		return "/dev/shm"
	}
	return os.TempDir()
}

// cleanTemp removes the processing directories a previous run left behind
// when it was stopped in the middle of a document.
func cleanTemp(root string) int {
	n := 0
	for _, r := range []string{root, os.TempDir()} {
		dirs, _ := filepath.Glob(filepath.Join(r, tempPrefix+"*"))
		for _, d := range dirs {
			if os.RemoveAll(d) == nil {
				n++
			}
		}
	}
	return n
}

type app struct {
	log     *slog.Logger
	maxSize int64

	// models holds one model per document processed at a time: the bias
	// is set on the model for each document. The instances share what
	// can be shared: the embedding table, mapped from the file, and the
	// tokenizer; each keeps its encoder layers (about 15 MB).
	models chan *indecis.Model
	// admitted counts the requests being processed or waiting for a
	// model; beyond limit, a request is refused before its document is
	// received, which would take memory in the processing directory.
	admitted atomic.Int64
	limit    int64

	tmp     string // see tempRoot
	results string // the documents waiting for their download

	mu    sync.Mutex
	files map[string]result
}

// result is a redacted document waiting for its download, in a file of
// the results directory: outside Go's memory, which would hold it until
// a collection and could not overwrite it reliably.
type result struct {
	name, path string
	expires    time.Time
}

func newApp(models []*indecis.Model, queue int, log *slog.Logger, maxSize int64) (*app, error) {
	a := &app{log: log, maxSize: maxSize, files: map[string]result{}, tmp: tempRoot(),
		models: make(chan *indecis.Model, len(models)), limit: int64(len(models) + queue)}
	for _, m := range models {
		a.models <- m
	}
	if n := cleanTemp(a.tmp); n > 0 {
		log.Info("removed processing directories left by a previous run", "count", n)
	}
	var err error
	if a.results, err = os.MkdirTemp(a.tmp, tempPrefix+"results-"); err != nil {
		return nil, err
	}
	go func() {
		for range time.Tick(time.Minute) {
			a.purge()
		}
	}()
	return a, nil
}

// purge erases the expired documents.
func (a *app) purge() {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, v := range a.files {
		if now.After(v.expires) {
			erase(v.path)
			delete(a.files, k)
		}
	}
}

// erase overwrites a file with zeros, then removes it: in /dev/shm, its
// bytes would otherwise stay in memory until the pages are reused.
func erase(path string) {
	if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		if st, err := f.Stat(); err == nil {
			io.CopyN(f, zeros{}, st.Size())
		}
		f.Close()
	}
	os.Remove(path)
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// redact processes the document of a multipart form, its fields bias
// and strategy in any order. The parts are read as they arrive, the
// document written straight into the processing directory: parsing the
// form would put a large document in the system's temporary directory,
// on disk. With ?progress, the response is a stream of JSON lines: the
// progress of a CSV file, then the result or the error.
func (a *app) redact(w http.ResponseWriter, r *http.Request) {
	if a.admitted.Add(1) > a.limit {
		a.admitted.Add(-1)
		httpError(w, http.StatusServiceUnavailable, "le serveur traite déjà le maximum de documents : réessayez dans quelques minutes")
		return
	}
	defer a.admitted.Add(-1)
	r.Body = http.MaxBytesReader(w, r.Body, a.maxSize+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		httpError(w, http.StatusBadRequest, "formulaire attendu")
		return
	}
	dir, err := os.MkdirTemp(a.tmp, tempPrefix)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.RemoveAll(dir) // the document never stays on disk
	var filename, ext, in string
	var size int64
	fields := map[string]string{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			httpError(w, http.StatusBadRequest, "document manquant ou trop volumineux")
			return
		}
		if part.FormName() != "document" {
			v, _ := io.ReadAll(io.LimitReader(part, 64))
			fields[part.FormName()] = string(v)
			continue
		}
		filename, ext = part.FileName(), strings.ToLower(filepath.Ext(part.FileName()))
		if _, ok := walkers[ext]; !ok && !isCSV(ext) {
			httpError(w, http.StatusBadRequest, "format non pris en charge : Word (.docx), OpenDocument (.odt) ou CSV (.csv, .tsv)")
			return
		}
		in = filepath.Join(dir, "in"+ext)
		f, err := os.Create(in)
		if err == nil {
			size, err = io.Copy(f, part)
			f.Close()
		}
		if err != nil {
			httpError(w, http.StatusBadRequest, "document manquant ou trop volumineux")
			return
		}
	}
	if in == "" {
		httpError(w, http.StatusBadRequest, "document manquant")
		return
	}
	bias, err := strconv.ParseFloat(fields["bias"], 64)
	if err != nil || bias < 0 || bias > 10 {
		bias = 5
	}
	strategy := anonymizer.Redact
	if fields["strategy"] == "tag" {
		strategy = anonymizer.TagReplace
	}

	var send func(any)
	if r.URL.Query().Has("progress") {
		w.Header().Set("Content-Type", "application/x-ndjson")
		rc := http.NewResponseController(w)
		enc := json.NewEncoder(w)
		send = func(v any) {
			enc.Encode(v)
			rc.Flush()
		}
	}
	var progress func(done, total int64)
	if send != nil {
		progress = func(done, total int64) {
			send(map[string]any{"progress": map[string]int64{"done": done, "total": total}})
		}
	}
	fail := func(code int, msg string) {
		if send != nil {
			send(map[string]string{"error": msg})
			return
		}
		httpError(w, code, msg)
	}

	out := filepath.Join(dir, "out"+ext)
	var m *indecis.Model
	select {
	case m = <-a.models:
	default:
		if send != nil {
			send(map[string]bool{"queued": true})
		}
		select {
		case m = <-a.models:
		case <-r.Context().Done():
			return
		}
	}
	start := time.Now()
	var res *response
	if isCSV(ext) {
		res, err = a.redactCSV(r.Context(), m, in, out, ext, size, bias, strategy, progress)
	} else {
		res, err = process(m, walkers[ext], in, out, bias, strategy)
	}
	elapsed := time.Since(start)
	a.models <- m
	if err != nil {
		a.log.Error("redaction", "format", ext, "error", err) // never the content
		fail(http.StatusUnprocessableEntity, "le document n'a pas pu être traité : "+err.Error())
		return
	}
	name := strings.TrimSuffix(filename, filepath.Ext(filename)) + "_caviarde" + ext
	if strategy == anonymizer.TagReplace {
		name = strings.TrimSuffix(filename, filepath.Ext(filename)) + "_pseudonymise" + ext
	}
	token, written, err := a.store(name, out)
	if err != nil {
		fail(http.StatusInternalServerError, err.Error())
		return
	}
	res.Name, res.Download, res.Seconds = name, "/api/download/"+token, elapsed.Seconds()
	a.log.Info("document redacted", "format", ext, "bytes", written, "elements", len(res.Elements), "duration", elapsed.Round(time.Millisecond))
	if send != nil {
		send(map[string]any{"result": res})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

// redactCSV redacts a CSV file from one file to the other, as a stream.
func (a *app) redactCSV(ctx context.Context, m *indecis.Model, in, out, ext string, size int64, bias float64, strategy anonymizer.Strategy, progress func(done, total int64)) (*response, error) {
	src, err := os.Open(in)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	dst, err := os.Create(out)
	if err != nil {
		return nil, err
	}
	res, err := processCSV(ctx, m, src, dst, ext, size, bias, strategy, progress)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	return res, err
}

// store moves the document produced to the results directory, on the
// same file system, and returns the token of its download and its size.
func (a *app) store(name, out string) (string, int64, error) {
	b := make([]byte, 16)
	rand.Read(b)
	token := hex.EncodeToString(b)
	path := filepath.Join(a.results, token)
	if err := os.Rename(out, path); err != nil {
		return "", 0, err
	}
	st, err := os.Stat(path)
	if err != nil {
		erase(path)
		return "", 0, err
	}
	a.purge()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.files[token] = result{name: name, path: path, expires: time.Now().Add(keep)}
	return token, st.Size(), nil
}

func (a *app) download(w http.ResponseWriter, r *http.Request) {
	// Served once: the document leaves the server with its download.
	a.mu.Lock()
	res, ok := a.files[r.PathValue("token")]
	delete(a.files, r.PathValue("token"))
	a.mu.Unlock()
	if ok {
		defer erase(res.path)
	}
	if !ok || time.Now().After(res.expires) {
		http.Error(w, "document déjà téléchargé ou expiré : recommencez", http.StatusNotFound)
		return
	}
	f, err := os.Open(res.path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	ctype := mime.TypeByExtension(filepath.Ext(res.name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": res.name}))
	io.Copy(w, f)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
