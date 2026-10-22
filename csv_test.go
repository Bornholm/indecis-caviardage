//go:build !js

package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bornholm/go-anon/pkg/anonymizer"
	"github.com/bornholm/go-anon/pkg/docprocessor"
	"github.com/bornholm/indecis"
)

// loadModel loads the model of CAVIARDAGE_MODEL, or the default one of
// the Makefile, and skips the test without it.
func loadModel(t testing.TB) *indecis.Model {
	dir := os.Getenv("CAVIARDAGE_MODEL")
	if dir == "" {
		dir = "../indecis-ner/runs/work-fam-p576"
	}
	if _, err := os.Stat(dir); err != nil {
		t.Skip("no model in", dir)
	}
	m, err := indecis.Load(dir, indecis.WithInt8())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestProcessCSV(t *testing.T) {
	m := loadModel(t)
	in := "\xef\xbb\xbfid;nom;prénom;courriel;téléphone;commentaire\r\n" +
		"1;DUPONT;Jean;jean.dupont@exemple.fr;06 12 34 56 78;\"Arrêt maladie; rappeler\r\nlundi\"\r\n" +
		"2;MARTIN;Sophie;sophie.martin@exemple.fr;06 98 76 54 32;RAS\r\n"
	for _, strategy := range []anonymizer.Strategy{anonymizer.Redact, anonymizer.TagReplace} {
		var out bytes.Buffer
		var calls int
		res, err := processCSV(context.Background(), m, strings.NewReader(in), &out, ".csv", int64(len(in)), settings{bias: 5, strategy: strategy},
			func(done, total int64) { calls++ })
		if err != nil {
			t.Fatal(err)
		}
		got := out.String()
		if !strings.HasPrefix(got, "\xef\xbb\xbfid;nom;prénom;") || !strings.Contains(got, "\r\n") {
			t.Errorf("BOM, separator or line ends lost:\n%s", got)
		}
		for _, leak := range []string{"DUPONT", "jean.dupont@exemple.fr", "06 12 34 56 78", "MARTIN", "06 98 76 54 32"} {
			if strings.Contains(got, leak) {
				t.Errorf("%q left in the output:\n%s", leak, got)
			}
		}
		r := csv.NewReader(strings.NewReader(strings.TrimPrefix(got, "\xef\xbb\xbf")))
		r.Comma = ';'
		records, err := r.ReadAll()
		if err != nil || len(records) != 3 || len(records[1]) != 6 {
			t.Errorf("output is not the same table: %v %v", err, records)
		}
		if res.Rows != 3 || res.Distinct == 0 || len(res.Elements) == 0 || calls == 0 {
			t.Errorf("rows %d, distinct %d, elements %d, progress calls %d", res.Rows, res.Distinct, len(res.Elements), calls)
		}
		t.Logf("%v\n%s", strategy, got)
	}
}

// BenchmarkProcessCSV measures the throughput on the file given by
// CAVIARDAGE_CSV.
func BenchmarkProcessCSV(b *testing.B) {
	path := os.Getenv("CAVIARDAGE_CSV")
	if path == "" {
		b.Skip("CAVIARDAGE_CSV not set")
	}
	m := loadModel(b)
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	if n, err := strconv.Atoi(os.Getenv("CAVIARDAGE_PARTS")); err == nil {
		csvParts = n
	}
	if n, err := strconv.Atoi(os.Getenv("CAVIARDAGE_PART_KB")); err == nil {
		csvPartBytes = n << 10
	}
	for b.Loop() {
		start := time.Now()
		var out bytes.Buffer
		res, err := processCSV(context.Background(), m, bytes.NewReader(data), &out, ".csv", int64(len(data)), settings{bias: 5, strategy: anonymizer.Redact}, nil)
		if err != nil {
			b.Fatal(err)
		}
		s := time.Since(start).Seconds()
		b.ReportMetric(float64(len(data))/s/1e3, "kB/s")
		b.Logf("%d rows, %d distinct, %d replaced, %.1f s, %d bytes produced", res.Rows, res.Distinct, res.Replaced, s, out.Len())
	}
}

// Two documents processed at the same time, each with its own instance
// of the model and its own bias, give the same result as one after the
// other: the instances share the embeddings and the tokenizer, not the
// bias.
func TestConcurrentInstances(t *testing.T) {
	models := []*indecis.Model{loadModel(t), loadModel(t)}
	data, err := os.ReadFile("demo/clients.csv")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	run := func(i int) (string, string) {
		var out bytes.Buffer
		bias := []float64{3, 7}[i]
		if _, err := processCSV(context.Background(), models[i], bytes.NewReader(data), &out, ".csv", int64(len(data)), settings{bias: bias, strategy: anonymizer.Redact}, nil); err != nil {
			t.Error(err)
		}
		docx := filepath.Join(dir, fmt.Sprintf("out%d.docx", i))
		if _, err := process(models[i], walkers[".docx"], "demo/compte-rendu.docx", docx, settings{bias: bias, strategy: anonymizer.Redact}); err != nil {
			t.Error(err)
		}
		text, err := docxText(docx)
		if err != nil {
			t.Error(err)
		}
		return out.String(), text
	}
	var seq [2][2]string
	for i := range 2 {
		seq[i][0], seq[i][1] = run(i)
	}
	if seq[0][0] == seq[1][0] {
		t.Fatal("the two biases should give different results")
	}
	var par [2][2]string
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Go(func() { par[i][0], par[i][1] = run(i) })
	}
	wg.Wait()
	if par != seq {
		t.Error("processed at the same time, the documents differ from one after the other")
	}
}

// docxText returns the text of a document's paragraphs.
func docxText(path string) (string, error) {
	w, err := walkers[".docx"](path)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	err = w.Walk(func(seg docprocessor.Segment) error {
		b.WriteString(seg.Text + "\n")
		return nil
	})
	return b.String(), err
}

// The terms and types kept by the page stay readable in a document, and
// out of the list; the rest is still masked.
func TestProcessRelease(t *testing.T) {
	m := loadModel(t)
	dir := t.TempDir()
	run := func(set settings) *response {
		res, err := process(m, walkers[".docx"], "demo/compte-rendu.docx", filepath.Join(dir, "out.docx"), set)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	before := run(settings{bias: 5, strategy: anonymizer.Redact})
	var term string
	types := map[string]int{}
	for _, e := range before.Elements {
		types[e.Type]++
		t.Logf("%-8s %.2f %-9s %q", e.Type, e.Confidence, e.Sensitive, e.Text)
		if term == "" && e.Type != "PERSON" && e.Sensitive == "" && len(e.Text) > 4 {
			term = e.Text
		}
	}
	after := run(settings{bias: 5, strategy: anonymizer.Redact, release: newReleaser([]string{term}, []string{"DATETIME"})})
	for _, e := range after.Elements {
		if e.Text == term || (e.Type == "DATETIME" && e.Sensitive == "") {
			t.Errorf("%s %q still masked", e.Type, e.Text)
		}
	}
	if len(after.Elements) >= len(before.Elements) || types["PERSON"] == 0 {
		t.Errorf("%d elements after, %d before (%v)", len(after.Elements), len(before.Elements), types)
	}
	t.Logf("kept %q and DATETIME: %d elements, %d before; warnings %q", term, len(after.Elements), len(before.Elements), after.Warnings)
	for _, e := range after.Redetected {
		t.Logf("redetected %q", e.Text)
	}
}
