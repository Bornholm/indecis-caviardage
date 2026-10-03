package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bornholm/go-anon/pkg/anonymizer"
	"github.com/bornholm/go-anon/pkg/docprocessor"
	pkgdocx "github.com/bornholm/go-anon/pkg/docx"
	"github.com/bornholm/go-anon/pkg/ner"
	pkgodt "github.com/bornholm/go-anon/pkg/odt"
	"github.com/bornholm/indecis"
	"github.com/bornholm/indecis-ner/recognizer"
)

var walkers = map[string]func(string) (docprocessor.Walker, error){
	".docx": pkgdocx.NewWalkerFromFile,
	".odt":  pkgodt.NewWalkerFromFile,
}

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

// element is a passage redacted, as the page lists it.
type element struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Sensitive string `json:"sensitive,omitempty"`
	Count     int    `json:"count"`
	Before    string `json:"before"`
	After     string `json:"after"`
}

type response struct {
	Name     string    `json:"name"`
	Download string    `json:"download"`
	Elements []element `json:"elements"`
	Replaced int       `json:"replaced"`
	Seconds  float64   `json:"seconds"`
	Warnings []string  `json:"warnings,omitempty"`
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
	res, err := a.process(open, in, out, bias, strategy)
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

// process redacts in into out with go-anon's document pipeline, the
// model finding the passages. The pipeline always pseudonymizes first:
// go-anon's mapping (placeholder -> original) is then exactly the list of
// what was replaced, with its type. In redaction mode, a second pass over
// the document turns the placeholders into blocks.
func (a *app) process(open func(string) (docprocessor.Walker, error), in, out string, bias float64, strategy anonymizer.Strategy) (*response, error) {
	a.m.SetSpanBias("mask", bias)
	a.m.SetSpanBias("sensitive", bias)
	rec := recognizer.New(a.m, []string{"mask", "sensitive"},
		ner.RegexEntityFilter(ner.BuiltinRegexPatterns), ner.RegexEntityFilter(ner.SecretPatterns()))
	sensitive := map[string]string{} // original text -> special category
	rec.OnFound = func(_ string, found []recognizer.Found) {
		for _, f := range found {
			if f.Sensitive != "" && !strings.ContainsAny(f.Text, "█⟦") {
				sensitive[f.Text] = f.Sensitive
			}
		}
	}
	anon := anonymizer.New(rec, anonymizer.Config{Strategy: anonymizer.TagReplace, ConsistentMap: true})
	proc := docprocessor.New(anon, docprocessor.WithDocumentVerification())
	walker, err := open(in)
	if err != nil {
		return nil, err
	}
	// The original text of each paragraph, in document order: the model
	// reads them as one document, and they give each element its count
	// and context.
	var paragraphs []string
	seen := map[string]bool{}
	if err := walker.Walk(func(seg docprocessor.Segment) error {
		if !seen[seg.Text] && strings.TrimSpace(seg.Text) != "" {
			seen[seg.Text] = true
			paragraphs = append(paragraphs, seg.Text)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := rec.Document(context.Background(), paragraphs); err != nil {
		return nil, err
	}
	session, report, err := proc.ProcessWithReport(walker, anonymizer.WithVerification())
	if err != nil {
		return nil, err
	}
	defer session.Close()
	res := &response{Replaced: report.TotalEntities(), Elements: elements(session.Mapping, paragraphs, sensitive)}
	if n := len(report.Leaks) + len(report.DocumentLeaks); n > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d passage(s) redétecté(s) après remplacement : relire le document", n))
	}
	if n := len(report.RegionLeaks); n > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d élément(s) dans du contenu non modifiable (images, champs), restés lisibles", n))
	}
	if strategy == anonymizer.Redact {
		err := walker.Walk(func(seg docprocessor.Segment) error {
			if redacted := placeholderRe.ReplaceAllString(seg.Text, "██████"); redacted != seg.Text {
				seg.Replace(redacted)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sr, err := docprocessor.Sanitize(walker, docprocessor.DefaultSanitizePolicy())
	if err != nil {
		return nil, err
	}
	if !sr.OK() {
		res.Warnings = append(res.Warnings, fmt.Sprintf("surfaces non traitées : %v", sr.Unprocessed))
	}
	saver, ok := walker.(interface{ SaveTo(string) error })
	if !ok {
		return nil, fmt.Errorf("format non enregistrable")
	}
	if err := saver.SaveTo(out); err != nil {
		return nil, err
	}
	if visual, err := proc.VerifyOutput(walker, out, session); err == nil && len(visual) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d élément(s) encore lisible(s) dans le document produit", len(visual)))
	}
	return res, nil
}

// placeholderRe matches go-anon's placeholders, ⟦TYPE_N_nonce⟧.
var placeholderRe = regexp.MustCompile(`⟦([A-Z][A-Z0-9_]*?)_\d+_[0-9a-f]+⟧`)

// elements lists the replaced passages from go-anon's mapping, in the
// order they first appear, with their count and context.
func elements(mapping map[string]string, paragraphs []string, sensitive map[string]string) []element {
	type found struct {
		el       element
		para, at int
	}
	var list []found
	for placeholder, original := range mapping {
		m := placeholderRe.FindStringSubmatch(placeholder)
		if m == nil || original == "" {
			continue
		}
		f := found{el: element{Type: m[1], Text: strings.TrimSpace(original), Sensitive: sensitive[original]}, para: len(paragraphs)}
		for i, p := range paragraphs {
			n := strings.Count(p, original)
			if n == 0 {
				continue
			}
			if f.el.Count == 0 {
				at := strings.Index(p, original)
				f.para, f.at = i, at
				f.el.Before, f.el.After = around(p[:at], true), around(p[at+len(original):], false)
			}
			f.el.Count += n
		}
		list = append(list, f)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].para != list[j].para {
			return list[i].para < list[j].para
		}
		return list[i].at < list[j].at
	})
	out := make([]element, len(list))
	for i, f := range list {
		out[i] = f.el
	}
	return out
}

// around returns up to 60 characters of context next to a passage, on
// its line.
func around(s string, before bool) string {
	const n = 60
	if before {
		if i := strings.LastIndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		for utf8.RuneCountInString(s) > n {
			_, size := utf8.DecodeRuneInString(s)
			s = s[size:]
		}
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	for utf8.RuneCountInString(s) > n {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
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
