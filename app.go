//go:build !js

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// keep is how long a redacted document waits to be downloaded, in secret
// memory. It is erased at its first download, or when it expires.
const keep = 15 * time.Minute

// tempPrefix names the directories a Word or ODT document is processed
// in, for the time of its processing: go-anon's DOCX and ODT readers only
// read files. A CSV file stays in secret memory.
const tempPrefix = "caviardage-"

// maxField bounds a form field other than the document: the terms to
// keep make the longest.
const maxField = 64 << 10

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

	tmp string // see tempRoot

	mu    sync.Mutex
	files map[string]result
}

// result is a redacted document waiting for its download, in secret
// memory (see secret).
type result struct {
	name    string
	data    *secret
	expires time.Time
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
			v.data.Close()
			delete(a.files, k)
		}
	}
}

// erase overwrites a file with zeros, then removes it: in /dev/shm, its
// bytes would otherwise stay in memory until the pages are reused.
// os.RemoveAll would only unlink it.
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

// redact processes the document of a multipart form, its fields bias,
// strategy, keep and release (see parseSettings) in any order. The parts are read as they arrive, the
// document copied straight into secret memory, where it waits for a
// model: parsing the form would put a large document in the system's
// temporary directory, on disk. With ?progress, the response is a stream
// of JSON lines: the progress of a CSV file, then the result or the error.
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
	doc := &secret{}
	defer doc.Close()
	var filename, ext string
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
			v, _ := io.ReadAll(io.LimitReader(part, maxField))
			fields[part.FormName()] = string(v)
			continue
		}
		filename, ext = part.FileName(), strings.ToLower(filepath.Ext(part.FileName()))
		if _, ok := walkers[ext]; !ok && !isCSV(ext) {
			httpError(w, http.StatusBadRequest, "format non pris en charge : Word (.docx), OpenDocument (.odt) ou CSV (.csv, .tsv)")
			return
		}
		if _, err := io.Copy(doc, part); errors.Is(err, errSecretFull) {
			a.log.Warn("secret memory full: raise the locked-memory limit (LimitMEMLOCK)")
			httpError(w, http.StatusServiceUnavailable, "le serveur n'a plus de mémoire protégée pour ce document : réessayez dans quelques minutes")
			return
		} else if err != nil {
			httpError(w, http.StatusBadRequest, "document manquant ou trop volumineux")
			return
		}
	}
	if filename == "" {
		httpError(w, http.StatusBadRequest, "document manquant")
		return
	}
	set := parseSettings(fields["bias"], fields["strategy"], fields["keep"], fields["release"])

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
	out := &secret{}
	var res *response
	if isCSV(ext) {
		res, err = processCSV(r.Context(), m, doc.Reader(), out, ext, doc.Size(), set, progress)
	} else {
		res, err = a.redactFile(m, doc, out, ext, set)
	}
	elapsed := time.Since(start)
	a.models <- m
	doc.Close() // the original is no longer needed
	if errors.Is(err, errSecretFull) {
		out.Close()
		a.log.Warn("secret memory full: raise the locked-memory limit (LimitMEMLOCK)")
		fail(http.StatusServiceUnavailable, "le serveur n'a plus de mémoire protégée pour ce document : réessayez dans quelques minutes")
		return
	}
	if err != nil {
		out.Close()
		a.log.Error("redaction", "format", ext, "error", err) // never the content
		fail(http.StatusUnprocessableEntity, "le document n'a pas pu être traité : "+err.Error())
		return
	}
	name := strings.TrimSuffix(filename, filepath.Ext(filename)) + "_caviarde" + ext
	if set.strategy == anonymizer.TagReplace {
		name = strings.TrimSuffix(filename, filepath.Ext(filename)) + "_pseudonymise" + ext
	}
	token := a.store(name, out)
	res.Name, res.Download, res.Seconds = name, "/api/download/"+token, elapsed.Seconds()
	a.log.Info("document redacted", "format", ext, "bytes", out.Size(), "elements", len(res.Elements), "duration", elapsed.Round(time.Millisecond))
	if send != nil {
		send(map[string]any{"result": res})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

// redactFile redacts a Word or ODT document through files, which
// go-anon's readers need: they exist in /dev/shm for the time of the
// processing only, then are overwritten with zeros.
func (a *app) redactFile(m *indecis.Model, doc, out *secret, ext string, set settings) (*response, error) {
	dir, err := os.MkdirTemp(a.tmp, tempPrefix)
	if err != nil {
		return nil, err
	}
	in, produced := filepath.Join(dir, "in"+ext), filepath.Join(dir, "out"+ext)
	defer func() {
		erase(in)
		erase(produced)
		os.RemoveAll(dir)
	}()
	f, err := os.OpenFile(in, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(f, doc.Reader())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	res, err := process(m, walkers[ext], in, produced, set)
	if err != nil {
		return nil, err
	}
	g, err := os.Open(produced)
	if err != nil {
		return nil, err
	}
	defer g.Close()
	if _, err := io.Copy(out, g); err != nil {
		return nil, err
	}
	return res, nil
}

// store keeps the document produced until its download and returns the
// token of the download.
func (a *app) store(name string, data *secret) string {
	b := make([]byte, 16)
	rand.Read(b)
	token := hex.EncodeToString(b)
	a.purge()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.files[token] = result{name: name, data: data, expires: time.Now().Add(keep)}
	return token
}

func (a *app) download(w http.ResponseWriter, r *http.Request) {
	// Served once: the document leaves the server with its download.
	a.mu.Lock()
	res, ok := a.files[r.PathValue("token")]
	delete(a.files, r.PathValue("token"))
	a.mu.Unlock()
	if ok {
		defer res.data.Close()
	}
	if !ok || time.Now().After(res.expires) {
		http.Error(w, "document déjà téléchargé ou expiré : recommencez", http.StatusNotFound)
		return
	}
	ctype := mime.TypeByExtension(filepath.Ext(res.name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": res.name}))
	w.Header().Set("Content-Length", strconv.FormatInt(res.data.Size(), 10))
	io.Copy(w, res.data.Reader())
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
