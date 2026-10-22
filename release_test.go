package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bornholm/go-anon/pkg/ner"
	"github.com/bornholm/indecis-ner/recognizer"
)

func TestReleaserApply(t *testing.T) {
	text := "Marc Petit, du service Comptabilité de Lyon, utilise JIRA et Jiraffe, à 80 %."
	at := func(s string) (int, int) {
		for i := range text {
			if len(text)-i >= len(s) && text[i:i+len(s)] == s {
				return i, i + len(s)
			}
		}
		t.Fatalf("%q not in the text", s)
		return 0, 0
	}
	mask := func(s, typ, sensitive string) recognizer.Found {
		start, end := at(s)
		return recognizer.Found{Entity: ner.Entity{Text: s, Type: ner.EntityType(typ), Start: start, End: end}, Sensitive: sensitive}
	}
	found := []recognizer.Found{
		mask("Marc Petit", "PERSON", ""),
		mask("service Comptabilité de Lyon", "ORG", ""),
		mask("JIRA", "MISC", ""),
		mask("Jiraffe", "MISC", "HEALTH"),
		mask("à 80 %", "QUANTITY", "HEALTH"),
	}
	texts := func(found []recognizer.Found) []string {
		var out []string
		for _, f := range found {
			if text[f.Start:f.End] != f.Text {
				t.Errorf("%q at %d-%d, the text has %q", f.Text, f.Start, f.End, text[f.Start:f.End])
			}
			out = append(out, f.Text)
		}
		return out
	}
	for _, c := range []struct {
		name         string
		terms, types []string
		want         []string
	}{
		{"nothing", nil, nil, []string{"Marc Petit", "service Comptabilité de Lyon", "JIRA", "Jiraffe", "à 80 %"}},
		// A term is cut out of the mask covering it, whatever its case,
		// as a whole word: not in « Jiraffe ». A sensitive mask is not cut.
		{"terms", []string{"jira", " service  comptabilité ", "80"}, nil, []string{"Marc Petit", "de Lyon", "Jiraffe", "à 80 %"}},
		// A released type keeps its sensitive masks; names are not
		// released by type, ORGANIZATION is go-anon's name of ORG.
		{"types", nil, []string{"ORGANIZATION", "MISC", "PERSON"}, []string{"Marc Petit", "Jiraffe", "à 80 %"}},
		// A term may release anything, a name or a whole sensitive mask
		// included.
		{"whole", []string{"Marc Petit", "Jiraffe", "À 80 %"}, nil, []string{"service Comptabilité de Lyon", "JIRA"}},
	} {
		r := newReleaser(c.terms, c.types)
		if got := texts(r.apply(text, found)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if newReleaser([]string{"", "  "}, []string{"PERSON", ""}) != nil {
		t.Error("a releaser without terms nor releasable types, want nil")
	}
}

func TestTrimmerApply(t *testing.T) {
	text := "Son fils aîné est suivi au CMP pour des troubles de l'attention. Son fils aîné est suivi au CMP."
	whole := "Son fils aîné est suivi au CMP pour des troubles de l'attention"
	mask := func(s string, from int) recognizer.Found {
		i := from + strings.Index(text[from:], s)
		return recognizer.Found{Entity: ner.Entity{Text: s, Type: "DEM", Start: i, End: i + len(s), Confidence: 0.43}, Sensitive: "HEALTH"}
	}
	found := []recognizer.Found{mask(whole, 0), mask("Son fils aîné est suivi au CMP", len(whole))}
	texts := func(found []recognizer.Found) []string {
		var out []string
		for _, f := range found {
			if text[f.Start:f.End] != f.Text || f.Sensitive != "HEALTH" || f.Confidence != 0.43 {
				t.Errorf("%+v: bounds, category or confidence lost", f)
			}
			out = append(out, f.Text)
		}
		return out
	}
	for _, c := range []struct {
		name, raw string
		want      []string
	}{
		// The parts stay masked, the rest is readable; the other mask,
		// whose text differs, is not trimmed.
		{"parts", `[{"text":" ` + whole + ` ","keep":["CMP","troubles de l'attention"]}]`,
			[]string{"CMP", "troubles de l'attention", "Son fils aîné est suivi au CMP"}},
		// A part out of order, or missing, keeps the mask whole.
		{"order", `[{"text":"` + whole + `","keep":["troubles","CMP"]}]`, []string{whole, "Son fils aîné est suivi au CMP"}},
		{"missing", `[{"text":"` + whole + `","keep":["CMP","hôpital"]}]`, []string{whole, "Son fils aîné est suivi au CMP"}},
		// A part trimmed in turn, on a later result.
		{"again", `[{"text":"` + whole + `","keep":["troubles de l'attention"]},{"text":"troubles de l'attention","keep":["attention"]}]`,
			[]string{"attention", "Son fils aîné est suivi au CMP"}},
	} {
		if got := texts(newTrimmer(c.raw).apply(text, found)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	for _, raw := range []string{"", "not json", `[{"text":"` + whole + `","keep":[]}]`, `[{"text":"","keep":["CMP"]}]`,
		`[{"text":"` + whole + `","keep":["` + whole + `"]}]`} {
		if newTrimmer(raw) != nil {
			t.Errorf("%q: a trimmer, want nil", raw)
		}
	}
	// What a trim leaves readable is cut out of the masks of the
	// verification passes, sensitive ones included.
	tr := newTrimmer(`[{"text":"` + whole + `","keep":["troubles de l'attention"]}]`)
	verified := "Son fils aîné est suivi au CMP pour des ⟦DEM_1_xxxx⟧."
	v := recognizer.Found{Entity: ner.Entity{Text: "Son fils aîné est suivi au CMP", Type: "DEM", Start: 0, End: len("Son fils aîné est suivi au CMP"), Confidence: 0.43}, Sensitive: "HEALTH"}
	if got := tr.freed.cut(verified, []recognizer.Found{v}, true); len(got) != 0 {
		t.Errorf("verification: %+v still masked", got)
	}
}
