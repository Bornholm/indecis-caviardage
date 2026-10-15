package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/bornholm/go-anon/pkg/anonymizer"
	pkgcsv "github.com/bornholm/go-anon/pkg/csv"
	"github.com/bornholm/go-anon/pkg/ner"
	"github.com/bornholm/indecis"
	"github.com/bornholm/indecis-ner/recognizer"
)

// A CSV file is read and written as a stream, by go-anon's csv.Stream,
// a batch of rows at a time: the model reads a document in one piece,
// which a file of several tens of megabytes does not afford. Within a
// batch the model reads the rows as one text, in windows spread over the
// cores; each row is read as lines « column : value », the header naming
// the column, so that « 06 12 34 56 78 » under « Téléphone » is not
// judged alone.
const (
	// The page lists the most frequent elements only, the counts being
	// those of the whole file; beyond maxTracked distinct elements, the
	// next ones are masked but no longer counted.
	maxListed     = 2000
	maxTracked    = 100_000
	maxRedetected = 200
	// A cell already read under the same column takes the passages found
	// the first time, without the model, as Document does within a text:
	// towns, categories, recurring remarks. Up to maxCached lines.
	maxCached = 200_000
)

// identifier returns the passage of a cell that go-anon's regular
// expressions find in full, such as an e-mail address or a phone number,
// shifted by at, the start of the value in its line: the model, which
// would mask it as well, does not read it. Identifiers cost it many
// tokens.
func identifier(value string, at int) ([]recognizer.Found, bool) {
	trimmed := strings.TrimSpace(value)
	start := strings.Index(value, trimmed)
	for _, e := range idPatterns(value, nil) {
		if e.Start <= start && e.End >= start+len(trimmed) {
			e.Start, e.End = e.Start+at, e.End+at
			return []recognizer.Found{{Entity: e}}, true
		}
	}
	return nil, false
}

var idPatterns = ner.RegexEntityFilter(ner.BuiltinRegexPatterns)

// csvParts is the number of parts of a batch the model reads at the same
// time, of about csvPartBytes of text each. Each part the model reads
// holds buffers in proportion to its windows of text: small parts keep
// the memory low (400 MB at most with these values, 2 GB with parts of
// 64 KB), at the same speed.
var csvParts, csvPartBytes = 4, 4 << 10

// isCSV tells whether a file name is processed by processCSV.
func isCSV(ext string) bool { return ext == ".csv" || ext == ".tsv" }

// newRecognizer prepares the model and go-anon's regular expressions with
// the given bias, as both pipelines use them.
func newRecognizer(m *indecis.Model, bias float64) *recognizer.Recognizer {
	m.SetSpanBias("mask", bias)
	m.SetSpanBias("sensitive", bias)
	return recognizer.New(m, []string{"mask", "sensitive"},
		ner.RegexEntityFilter(ner.BuiltinRegexPatterns), ner.RegexEntityFilter(ner.SecretPatterns()))
}

// preset is the anonymizer's recognizer for one cell: the passages were
// found beforehand, in the row and the batch.
type preset struct{ found []ner.Entity }

func (p *preset) Recognize(string) ([]ner.Entity, error) { return p.found, nil }

// csvRow is a record and, for each non-empty cell, the line the model
// reads and where the value starts in it.
type csvRow struct {
	cells, lines []string
	at           []int
}

// counted is an element of the list with its order of appearance.
type counted struct {
	el    element
	order int
}

// processCSV redacts a CSV or TSV file from in to out, without holding
// it in memory. size is the length of in, for progress, which receives
// the bytes read so far after each batch.
func processCSV(ctx context.Context, m *indecis.Model, in io.Reader, out io.Writer, ext string, size int64, bias float64, strategy anonymizer.Strategy, progress func(done, total int64)) (*response, error) {
	// The parts of a batch are read by the model at the same time, each by
	// its own recognizer: it keeps what it found in its part.
	var found []recognizer.Found
	recs := make([]*recognizer.Recognizer, csvParts)
	for k := range recs {
		recs[k] = newRecognizer(m, bias)
		recs[k].OnFound = func(_ string, f []recognizer.Found) { found = f }
	}
	cell := &preset{}
	// In redaction mode go-anon keeps no table of what it replaced: the
	// list is built from the entities of each cell. Fixed-length blocks,
	// as in documents.
	config := anonymizer.Config{Strategy: strategy, ConsistentMap: true}
	if strategy == anonymizer.Redact {
		config.RedactMinRunes, config.RedactMaxRunes = 6, 6
	}
	anon := anonymizer.New(cell, config)
	session := anonymizer.NewSession()
	defer session.Close()

	var opts []pkgcsv.StreamOption
	if ext == ".tsv" {
		opts = append(opts, pkgcsv.WithSeparator('\t'))
	}
	opts = append(opts, pkgcsv.WithBatchBytes(csvParts*csvPartBytes))
	stream, err := pkgcsv.NewStream(in, out, opts...)
	if err != nil {
		return nil, err
	}

	res := &response{}
	list := map[string]*counted{}
	cached := map[string][]recognizer.Found{}
	direct := map[string][]recognizer.Found{}
	var batch []csvRow
	// go-anon's stream keeps the header as is, and it names the columns of
	// the rows: the model, reading « nom » or « téléphone » alone, would
	// mask it. Without one, the first row is redacted like the others.
	err = stream.WalkRows(func(rows []pkgcsv.Row) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		labels := stream.Header()
		batch = batch[:0]
		clear(direct)
		for _, r := range rows {
			row := csvRow{cells: r.Cells, lines: make([]string, len(r.Cells)), at: make([]int, len(r.Cells))}
			for j, v := range r.Cells {
				if strings.TrimSpace(v) == "" {
					continue
				}
				prefix := ""
				if j < len(labels) && strings.TrimSpace(labels[j]) != "" {
					prefix = strings.TrimSpace(labels[j]) + " : "
				}
				row.lines[j], row.at[j] = prefix+v, len(prefix)
				if f, ok := identifier(v, len(prefix)); ok {
					direct[row.lines[j]] = f
				}
			}
			batch = append(batch, row)
		}
		part := func(i int) int { return i * len(recs) / len(batch) }
		paragraphs := make([][]string, len(recs))
		for i, row := range batch {
			k := part(i)
			for _, l := range row.lines {
				_, known := cached[l]
				if _, ok := direct[l]; ok {
					known = true
				}
				if l != "" && !known {
					paragraphs[k] = append(paragraphs[k], l)
				}
			}
			paragraphs[k] = append(paragraphs[k], "") // a blank line between rows
		}
		errs := make([]error, len(recs))
		var wg sync.WaitGroup
		for k, p := range paragraphs {
			if strings.TrimSpace(strings.Join(p, "")) == "" {
				continue
			}
			wg.Go(func() { errs[k] = recs[k].Document(ctx, p) })
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return err
		}
		for i, row := range batch {
			rec := recs[part(i)]
			for j, value := range row.cells {
				if row.lines[j] == "" {
					continue
				}
				found = nil
				if c, ok := cached[row.lines[j]]; ok {
					found = c
				} else if d, ok := direct[row.lines[j]]; ok {
					found = d
				} else if _, err := rec.Recognize(row.lines[j]); err != nil {
					return err
				} else if len(cached) < maxCached {
					cached[row.lines[j]] = found
				}
				// The passages of the line, cut to the value: the column
				// name is only there for context.
				cell.found = cell.found[:0]
				sensitive := map[int]string{}
				for _, f := range found {
					s, e := max(f.Start, row.at[j])-row.at[j], f.End-row.at[j]
					if s >= e || strings.TrimSpace(value[s:e]) == "" {
						continue
					}
					ent := f.Entity
					ent.Start, ent.End, ent.Text = s, e, value[s:e]
					if f.Sensitive != "" {
						sensitive[s] = f.Sensitive
					}
					cell.found = append(cell.found, ent)
				}
				if len(cell.found) == 0 {
					continue
				}
				result, err := anon.Anonymize(value, anonymizer.WithSession(session), anonymizer.WithVerification())
				if err != nil {
					return fmt.Errorf("ligne %d : %w", rows[i].Line, err)
				}
				row.cells[j] = result.Text
				res.Replaced += len(result.Entities)
				for _, ent := range result.Entities {
					k := string(ent.Type) + "\x00" + ent.Text
					if c, ok := list[k]; ok {
						c.el.Count++
						continue
					}
					if len(list) >= maxTracked {
						continue
					}
					label := row.lines[j][:row.at[j]]
					list[k] = &counted{order: len(list), el: element{
						Type: string(ent.Type), Text: strings.TrimSpace(ent.Text), Sensitive: sensitive[ent.Start], Count: 1,
						Before: around(label+value[:ent.Start], true), After: around(value[ent.End:], false),
					}}
				}
				if result.Verification != nil {
					for _, l := range result.Verification.Leaks {
						if len(res.Redetected) < maxRedetected && l.Start < l.End && l.End <= len(result.Text) {
							blocks := func(s string) string { return placeholderRe.ReplaceAllString(s, "██") }
							res.Redetected = append(res.Redetected, element{Text: result.Text[l.Start:l.End],
								Before: around(blocks(result.Text[:l.Start]), true), After: around(blocks(result.Text[l.End:]), false)})
						}
					}
				}
			}
		}
		res.Rows = rows[len(rows)-1].Line
		if progress != nil {
			progress(stream.BytesRead(), size)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	all := make([]*counted, 0, len(list))
	for _, c := range list {
		all = append(all, c)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].el.Count != all[j].el.Count {
			return all[i].el.Count > all[j].el.Count
		}
		return all[i].order < all[j].order
	})
	res.Distinct = len(all)
	for _, c := range all[:min(len(all), maxListed)] {
		res.Elements = append(res.Elements, c.el)
	}
	if res.Elements == nil {
		res.Elements = []element{}
	}
	if len(all) > maxListed {
		res.Warnings = append(res.Warnings, fmt.Sprintf("La liste ne montre que les %d éléments les plus fréquents, sur %d distincts.", maxListed, len(all)))
	}
	if len(list) >= maxTracked {
		res.Warnings = append(res.Warnings, fmt.Sprintf("Au-delà de %d éléments distincts, les suivants sont masqués sans être comptés.", maxTracked))
	}
	if n := len(res.Redetected); n > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("La vérification a relevé %d passage(s) restés en clair, listés ci-dessous.", n))
	}
	return res, nil
}
