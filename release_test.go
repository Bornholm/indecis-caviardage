package main

import (
	"reflect"
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
