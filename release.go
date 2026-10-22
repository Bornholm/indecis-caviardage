package main

import (
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
	if r == nil {
		return found
	}
	kept := r.occurrences(text)
	var out []recognizer.Found
	for _, f := range found {
		if r.byType(f) {
			continue
		}
		if f.Sensitive != "" {
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
	// onFound receives what is masked in each text, as the recognizer's
	// OnFound.
	onFound func(text string, found []recognizer.Found)
}

func newReleasing(rec *recognizer.Recognizer, release *releaser) *releasing {
	r := &releasing{rec: rec, release: release, seen: map[string]bool{}}
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
// to pseudonymize), the terms to keep, one per line, and the types to
// release, separated by commas.
func parseSettings(bias, strategy, keep, release string) settings {
	set := settings{bias: 5, strategy: anonymizer.Redact}
	if b, err := strconv.ParseFloat(bias, 64); err == nil && b >= 0 && b <= 10 {
		set.bias = b
	}
	if strategy == "tag" {
		set.strategy = anonymizer.TagReplace
	}
	set.release = newReleaser(strings.Split(keep, "\n"), strings.Split(release, ","))
	return set
}
