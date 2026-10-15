package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/bornholm/go-anon/pkg/anonymizer"
	"github.com/bornholm/go-anon/pkg/docprocessor"
	pkgdocx "github.com/bornholm/go-anon/pkg/docx"
	pkgodt "github.com/bornholm/go-anon/pkg/odt"
	"github.com/bornholm/indecis"
	"github.com/bornholm/indecis-ner/recognizer"
)

var walkers = map[string]func(string) (docprocessor.Walker, error){
	".docx": pkgdocx.NewWalkerFromFile,
	".odt":  pkgodt.NewWalkerFromFile,
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
	// Distinct and Rows are set for CSV files, whose list is cut to the
	// most frequent elements.
	Distinct int      `json:"distinct,omitempty"`
	Rows     int      `json:"rows,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	// Redetected lists what go-anon's verification found again in the
	// output, outside the replaced passages: to check by eye.
	Redetected []element `json:"redetected,omitempty"`
}

// process redacts in into out with go-anon's document pipeline, the
// model finding the passages. The pipeline always pseudonymizes first:
// go-anon's mapping (placeholder -> original) is then exactly the list of
// what was replaced, with its type. In redaction mode, a second pass over
// the document turns the placeholders into blocks.
func process(m *indecis.Model, open func(string) (docprocessor.Walker, error), in, out string, bias float64, strategy anonymizer.Strategy) (*response, error) {
	rec := newRecognizer(m, bias)
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
	if err := read(context.Background(), rec, paragraphs); err != nil {
		return nil, err
	}
	session, report, err := proc.ProcessWithReport(walker, anonymizer.WithVerification())
	if err != nil {
		return nil, err
	}
	defer session.Close()
	res := &response{Replaced: report.TotalEntities(), Elements: elements(session.Mapping, paragraphs, sensitive)}
	res.Redetected = redetected(walker, report)
	if n := len(res.Redetected); n > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("La vérification a relevé %d passage(s) restés en clair, listés ci-dessous : intitulés sans risque, ou oublis à masquer à la main.", n))
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

// redetected reads, in the pseudonymized document, the passages go-anon's
// verification found outside the replacements: per paragraph, and in the
// document recomposed with spaces, as the verification builds it. The
// placeholders in their context show as blocks.
func redetected(walker docprocessor.Walker, report *docprocessor.Report) []element {
	var outputs []string
	walker.Walk(func(seg docprocessor.Segment) error {
		outputs = append(outputs, seg.Text)
		return nil
	})
	joined := strings.Join(outputs, " ")
	var out []element
	seen := map[string]bool{}
	add := func(text string, start, end int) {
		if start < 0 || end > len(text) || start >= end {
			return
		}
		passage := strings.TrimSpace(text[start:end])
		if passage == "" || seen[passage] {
			return
		}
		seen[passage] = true
		blocks := func(s string) string { return placeholderRe.ReplaceAllString(s, "██") }
		out = append(out, element{Type: "", Text: passage,
			Before: around(blocks(text[:start]), true), After: around(blocks(text[end:]), false)})
	}
	for _, l := range report.Leaks {
		if l.Segment < len(outputs) {
			add(outputs[l.Segment], l.Start, l.End)
		}
	}
	for _, l := range report.DocumentLeaks {
		add(joined, l.Start, l.End)
	}
	return out
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
