//go:build !js

package main

import (
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
	m       *indecis.Model
	log     *slog.Logger
	maxSize int64

	// One document at a time: the model's bias is set for each, and a
	// document already takes every core.
	work sync.Mutex

	tmp string // see tempRoot

	mu    sync.Mutex
	files map[string]result
}

type result struct {
	name    string
	data    []byte
	expires time.Time
}

func newApp(m *indecis.Model, log *slog.Logger, maxSize int64) *app {
	a := &app{m: m, log: log, maxSize: maxSize, files: map[string]result{}, tmp: tempRoot()}
	if n := cleanTemp(a.tmp); n > 0 {
		log.Info("removed processing directories left by a previous run", "count", n)
	}
	go func() {
		for range time.Tick(time.Minute) {
			a.purge()
		}
	}()
	return a
}

// purge forgets the expired documents, their bytes overwritten first: Go
// does not clear the memory it frees.
func (a *app) purge() {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, v := range a.files {
		if now.After(v.expires) {
			clear(v.data)
			delete(a.files, k)
		}
	}
}

func (a *app) redact(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, a.maxSize+1<<20)
	file, header, err := r.FormFile("document")
	if err != nil {
		httpError(w, http.StatusBadRequest, "document manquant ou trop volumineux")
		return
	}
	defer file.Close()
	ext := strings.ToLower(filepath.Ext(header.Filename))
	open, ok := walkers[ext]
	if !ok {
		httpError(w, http.StatusBadRequest, "format non pris en charge : Word (.docx) ou OpenDocument (.odt)")
		return
	}
	bias, err := strconv.ParseFloat(r.FormValue("bias"), 64)
	if err != nil || bias < 0 || bias > 10 {
		bias = 5
	}
	strategy := anonymizer.Redact
	if r.FormValue("strategy") == "tag" {
		strategy = anonymizer.TagReplace
	}

	dir, err := os.MkdirTemp(a.tmp, tempPrefix)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.RemoveAll(dir) // the document never stays on disk
	in, out := filepath.Join(dir, "in"+ext), filepath.Join(dir, "out"+ext)
	f, err := os.Create(in)
	if err == nil {
		_, err = io.Copy(f, file)
		f.Close()
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}

	a.work.Lock()
	start := time.Now()
	res, err := process(a.m, open, in, out, bias, strategy)
	elapsed := time.Since(start)
	a.work.Unlock()
	if err != nil {
		a.log.Error("redaction", "error", err) // never the content
		httpError(w, http.StatusUnprocessableEntity, "le document n'a pas pu être traité : "+err.Error())
		return
	}
	data, err := os.ReadFile(out)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := strings.TrimSuffix(header.Filename, ext) + "_caviarde" + ext
	if strategy == anonymizer.TagReplace {
		name = strings.TrimSuffix(header.Filename, ext) + "_pseudonymise" + ext
	}
	token := a.store(name, data)
	res.Name, res.Download, res.Seconds = name, "/api/download/"+token, elapsed.Seconds()
	a.log.Info("document redacted", "format", ext, "bytes", len(data), "elements", len(res.Elements), "duration", elapsed.Round(time.Millisecond))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

func (a *app) store(name string, data []byte) string {
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
	// Served once: the document leaves the server's memory with its
	// download.
	a.mu.Lock()
	res, ok := a.files[r.PathValue("token")]
	delete(a.files, r.PathValue("token"))
	a.mu.Unlock()
	if !ok || time.Now().After(res.expires) {
		http.Error(w, "document déjà téléchargé ou expiré : recommencez", http.StatusNotFound)
		return
	}
	defer clear(res.data)
	ctype := mime.TypeByExtension(filepath.Ext(res.name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": res.name}))
	w.Write(res.data)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
