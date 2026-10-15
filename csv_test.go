//go:build !js

package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bornholm/go-anon/pkg/anonymizer"
	"github.com/bornholm/indecis"
)

// loadModel loads the model of CAVIARDAGE_MODEL, or the default one of
// the Makefile, and skips the test without it.
func loadModel(t testing.TB) *indecis.Model {
	dir := os.Getenv("CAVIARDAGE_MODEL")
	if dir == "" {
		dir = "../indecis-ner/runs/work-fam"
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
		res, err := processCSV(context.Background(), m, strings.NewReader(in), &out, ".csv", int64(len(in)), 5, strategy,
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
		res, err := processCSV(context.Background(), m, bytes.NewReader(data), &bytes.Buffer{}, ".csv", int64(len(data)), 5, anonymizer.Redact, nil)
		if err != nil {
			b.Fatal(err)
		}
		s := time.Since(start).Seconds()
		b.ReportMetric(float64(len(data))/s/1e3, "kB/s")
		b.Logf("%d rows, %d distinct, %d replaced, %.1f s", res.Rows, res.Distinct, res.Replaced, s)
	}
}
