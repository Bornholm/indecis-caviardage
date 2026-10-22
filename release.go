package main

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bornholm/go-anon/pkg/anonymizer"
	"github.com/bornholm/go-anon/pkg/ner"
	"github.com/bornholm/indecis-ner/recognizer"
)

// settings are the choices of the page for one document.
type settings struct {
	bias     float64
	strategy anonymizer.Strategy
	release  *releaser
	trim     *trimmer
}

// releasable are the types a whole category of masks can be released
// for: the model's quasi-identifiers. Names, codes and the identifiers
// of go-anon's regular expressions are released one term at a time only.
var releasable = map[string]bool{"LOC": true, "ORG": true, "DEM": true, "DATETIME": true, "QUANTITY": true, "MISC": true}

// aliases are the names go-anon gives the model's types in its
// placeholders, which the page lists.
var aliases = map[string]string{"LOCATION": "LOC", "ORGANIZATION": "ORG"}

// Limits on what the page sends: a list of terms, not a document.
const (
	maxTerms     = 2000
	maxTermRunes = 200
	// maxMaskRunes bounds the text of a mask to trim, longer than a term.
	maxMaskRunes = 2000
)

// releaser leaves readable what the user chose to keep: the terms given,
// wherever a mask covers them, and the masks of the released types,
// unless they cover a special category of data (GDPR article 9 or 10).
type releaser struct {
	terms *regexp.Regexp
	types map[string]bool
}

// newReleaser prepares the terms and types to keep readable. Unknown or
// protected types are ignored; nil means nothing to release.
func newReleaser(terms, types []string) *releaser {
	r := &releaser{types: map[string]bool{}}
	for _, t := range types {
		t = strings.TrimSpace(t)
		if a, ok := aliases[t]; ok {
			t = a
		}
		if releasable[t] {
			r.types[t] = true
		}
	}
	var patterns []string
	seen := map[string]bool{}
	for _, t := range terms {
		words := strings.Fields(t)
		if len(words) == 0 || utf8.RuneCountInString(t) > maxTermRunes || len(patterns) >= maxTerms {
			continue
		}
		for i, w := range words {
			words[i] = regexp.QuoteMeta(w)
		}
		// Spaces match any spacing, line breaks of a CSV cell included.
		p := strings.Join(words, `\s+`)
		if k := strings.ToLower(p); !seen[k] {
			seen[k] = true
			patterns = append(patterns, p)
		}
	}
	if len(patterns) > 0 {
		// Longest first: the alternation takes the first that matches.
		slices.SortStableFunc(patterns, func(a, b string) int { return len(b) - len(a) })
		r.terms = regexp.MustCompile(`(?i)` + strings.Join(patterns, "|"))
	}
	if r.terms == nil && len(r.types) == 0 {
		return nil
	}
	return r
}

// span is a passage of a text, in bytes.
type span struct{ start, end int }

// occurrences returns where the terms occur in text as whole words: «
// Jira » is kept in « Jira, » but not in « Jiraffe ».
func (r *releaser) occurrences(text string) []span {
	if r.terms == nil {
		return nil
	}
	word := func(c rune) bool { return unicode.IsLetter(c) || unicode.IsDigit(c) }
	var out []span
	for _, m := range r.terms.FindAllStringIndex(text, -1) {
		first, _ := utf8.DecodeRuneInString(text[m[0]:])
		last, _ := utf8.DecodeLastRuneInString(text[:m[1]])
		before, _ := utf8.DecodeLastRuneInString(text[:m[0]])
		after, _ := utf8.DecodeRuneInString(text[m[1]:])
		if (word(first) && m[0] > 0 && word(before)) || (word(last) && m[1] < len(text) && word(after)) {
			continue
		}
		out = append(out, span{m[0], m[1]})
	}
	return out
}

// apply removes from found, the passages of text to mask, what the user
// keeps: the masks of released types, and the terms, cut out of the masks
// that cover them. What remains of a mask on either side of a term stays
// masked. A mask of a special category is not cut: it is released only
// when a term covers it whole, « 80 » leaving « temps partiel
// thérapeutique (80 %) » masked.
func (r *releaser) apply(text string, found []recognizer.Found) []recognizer.Found {
	return r.cut(text, found, false)
}

// cut is apply; with sensitive, the terms are cut out of the masks of a
// special category too.
func (r *releaser) cut(text string, found []recognizer.Found, sensitive bool) []recognizer.Found {
	if r == nil {
		return found
	}
	kept := r.occurrences(text)
	var out []recognizer.Found
	for _, f := range found {
		if r.byType(f) {
			continue
		}
		if f.Sensitive != "" && !sensitive {
			whole := trim(text, span{f.Start, f.End})
			if !slices.ContainsFunc(kept, func(k span) bool { return k.start <= whole.start && k.end >= whole.end }) {
				out = append(out, f)
			}
			continue
		}
		pieces := []span{{f.Start, f.End}}
		for _, k := range kept {
			var next []span
			for _, p := range pieces {
				if k.end <= p.start || k.start >= p.end {
					next = append(next, p)
					continue
				}
				next = append(next, span{p.start, k.start}, span{k.end, p.end})
			}
			pieces = next
		}
		for _, p := range pieces {
			p = trim(text, p)
			if p.start >= p.end {
				continue
			}
			g := f
			g.Start, g.End, g.Text = p.start, p.end, text[p.start:p.end]
			out = append(out, g)
		}
	}
	return out
}

// byType tells whether a mask is released by its type.
func (r *releaser) byType(f recognizer.Found) bool {
	return r != nil && f.Sensitive == "" && r.types[string(f.Type)]
}

// trim narrows a piece of a mask to its first and last letter or digit:
// the spaces and punctuation around a term released are left readable.
// A piece without any is dropped.
func trim(text string, p span) span {
	if p.start >= p.end {
		return p
	}
	s := text[p.start:p.end]
	i := strings.IndexFunc(s, func(c rune) bool { return unicode.IsLetter(c) || unicode.IsDigit(c) })
	if i < 0 {
		return span{}
	}
	j := strings.LastIndexFunc(s, func(c rune) bool { return unicode.IsLetter(c) || unicode.IsDigit(c) })
	_, size := utf8.DecodeRuneInString(s[j:])
	return span{p.start + i, p.start + j + size}
}

// trimmer narrows the masks the user trimmed on the page: a mask whose
// text is one of them is replaced by the parts that stay masked. A mask
// is trimmed only if its text is the one the page showed, and its parts
// are found in it in order; otherwise it stays whole.
type trimmer struct {
	parts map[string][]string // mask text -> the parts that stay masked
	// freed holds what the trims leave readable, as terms, for go-anon's
	// verification passes only (see releasing).
	freed *releaser
}

// newTrimmer reads the page's trims, a JSON list of {"text", "keep"}:
// the text of a mask and the parts of it that stay masked, in order. A
// trim with nothing left masked is a release, which the page sends as a
// term. nil means nothing to trim.
func newTrimmer(raw string) *trimmer {
	var list []struct {
		Text string   `json:"text"`
		Keep []string `json:"keep"`
	}
	if json.Unmarshal([]byte(raw), &list) != nil {
		return nil
	}
	t := &trimmer{parts: map[string][]string{}}
	var freed []string
	for _, e := range list {
		text := strings.TrimSpace(e.Text)
		if len(t.parts) >= maxTerms || text == "" || utf8.RuneCountInString(text) > maxMaskRunes {
			continue
		}
		var parts, gaps []string
		at := 0
		for _, p := range e.Keep {
			p = strings.TrimSpace(p)
			i := strings.Index(text[at:], p)
			if p == "" || i < 0 {
				parts = nil
				break
			}
			gaps = append(gaps, text[at:at+i])
			parts = append(parts, p)
			at += i + len(p)
		}
		// Nothing left readable is no trim, and would trim forever.
		if len(parts) == 0 || len(parts) == 1 && parts[0] == text {
			continue
		}
		t.parts[text] = parts
		freed = append(freed, append(gaps, text[at:])...)
	}
	if len(t.parts) == 0 {
		return nil
	}
	t.freed = newReleaser(freed, nil)
	return t
}

// apply replaces each trimmed mask of found by its parts, which keep its
// type, category and confidence. A part trimmed in turn, on a later
// result, is trimmed too: each part is shorter than its mask, so this
// ends.
func (t *trimmer) apply(text string, found []recognizer.Found) []recognizer.Found {
	if t == nil {
		return found
	}
	var out []recognizer.Found
	for _, f := range found {
		pieces := t.pieces(text, f)
		if pieces == nil {
			out = append(out, f)
			continue
		}
		for _, p := range pieces {
			g := f
			g.Start, g.End, g.Text = p.start, p.end, text[p.start:p.end]
			out = append(out, t.apply(text, []recognizer.Found{g})...)
		}
	}
	return out
}

// pieces locates the parts of a trimmed mask in text, nil if the mask is
// not trimmed or a part is missing.
func (t *trimmer) pieces(text string, f recognizer.Found) []span {
	if f.Start < 0 || f.End > len(text) || f.Start >= f.End {
		return nil
	}
	whole := text[f.Start:f.End]
	trimmed := strings.TrimSpace(whole)
	parts, ok := t.parts[trimmed]
	if !ok {
		return nil
	}
	at := f.Start + strings.Index(whole, trimmed)
	var out []span
	for _, p := range parts {
		i := strings.Index(text[at:f.End], p)
		if i < 0 {
			return nil
		}
		out = append(out, span{at + i, at + i + len(p)})
		at += i + len(p)
	}
	return out
}

// releasing is the anonymizer's recognizer for documents: the model's
// passages, less what the user keeps. go-anon's verification passes read
// through it too, so that a passage kept is not reported as a leak.
type releasing struct {
	rec     *recognizer.Recognizer
	release *releaser
	found   []recognizer.Found
	// typed holds the texts released by their type, as terms: the model,
	// reading them again among placeholders in a verification pass, may
	// type them otherwise, or a part of them only.
	typed      *releaser
	typedTexts []string
	seen       map[string]bool
	trim       *trimmer
	// onFound receives what is masked in each text, as the recognizer's
	// OnFound.
	onFound func(text string, found []recognizer.Found)
}

func newReleasing(rec *recognizer.Recognizer, set settings) *releasing {
	r := &releasing{rec: rec, release: set.release, trim: set.trim, seen: map[string]bool{}}
	rec.OnFound = func(_ string, found []recognizer.Found) { r.found = found }
	return r
}

// Recognize implements anonymizer.Recognizer.
func (r *releasing) Recognize(text string) ([]ner.Entity, error) {
	r.found = nil
	if _, err := r.rec.Recognize(text); err != nil {
		return nil, err
	}
	grown := false
	for _, f := range r.found {
		if r.release.byType(f) && !r.seen[f.Text] {
			r.seen[f.Text] = true
			r.typedTexts = append(r.typedTexts, f.Text)
			grown = true
		}
	}
	if grown {
		r.typed = newReleaser(r.typedTexts, nil)
	}
	var found []recognizer.Found
	for _, f := range r.release.apply(text, r.found) {
		// A special category stays masked, as it does by type.
		if f.Sensitive != "" {
			found = append(found, f)
		} else {
			found = append(found, r.typed.apply(text, []recognizer.Found{f})...)
		}
	}
	found = r.trim.apply(text, found)
	// A verification pass reads the document with its placeholders: what
	// a trim left readable around them is not a leak, whatever the model
	// makes of it there, sensitive masks included. Nowhere else, though:
	// the words a trim left readable in one mask stay masked elsewhere.
	if r.trim != nil && strings.Contains(text, "⟦") {
		found = r.trim.freed.cut(text, found, true)
	}
	if r.onFound != nil {
		r.onFound(text, found)
	}
	out := make([]ner.Entity, len(found))
	for i, f := range found {
		out[i] = f.Entity
	}
	return out, nil
}

// parseSettings reads the page's fields: the bias, the strategy ("tag"
// to pseudonymize), the terms to keep, one per line, the types to
// release, separated by commas, and the masks to trim (newTrimmer).
func parseSettings(bias, strategy, keep, release, trim string) settings {
	set := settings{bias: 5, strategy: anonymizer.Redact}
	if b, err := strconv.ParseFloat(bias, 64); err == nil && b >= 0 && b <= 10 {
		set.bias = b
	}
	if strategy == "tag" {
		set.strategy = anonymizer.TagReplace
	}
	set.release = newReleaser(strings.Split(keep, "\n"), strings.Split(release, ","))
	set.trim = newTrimmer(trim)
	return set
}
