package main

import (
	"strings"
	"testing"
)

// A short passage is counted where go-anon replaced it, not wherever its
// text occurs: "m" inside "Comptoir" or "matin" is not a replacement.
func TestElementsCountPlaceholders(t *testing.T) {
	mapping := map[string]string{
		"⟦MISC_1_ab12⟧":   "m",
		"⟦PERSON_1_ab12⟧": "Jean Mercier",
	}
	outputs := []string{
		"Comptoir de Commerce, le matin.",
		"⟦PERSON_1_ab12⟧ ⟦MISC_1_ab12⟧'a dit qu'il ferait vite.",
	}
	got := elements(mapping, outputs, nil)
	if len(got) != 2 {
		t.Fatalf("%d elements, want 2: %+v", len(got), got)
	}
	person, misc := got[0], got[1]
	if person.Text != "Jean Mercier" || person.Count != 1 {
		t.Errorf("first element %+v, want Jean Mercier once", person)
	}
	if misc.Text != "m" || misc.Count != 1 {
		t.Errorf("second element %+v, want m once", misc)
	}
	if misc.Before != "Jean Mercier " || misc.After != "'a dit qu'il ferait vite." {
		t.Errorf("context %q / %q, want the original text around the replacement", misc.Before, misc.After)
	}
}

func TestAround(t *testing.T) {
	long := strings.Repeat("é", 100)
	for _, c := range []struct {
		s      string
		before bool
		want   string
	}{
		{"court", true, "court"},
		{"court", false, "court"},
		{"ligne 1\nligne 2", true, "ligne 2"},
		{"ligne 1\nligne 2", false, "ligne 1"},
		{"a" + long, true, strings.Repeat("é", 60)},
		{long + "a", false, strings.Repeat("é", 60)},
	} {
		if got := around(c.s, c.before); got != c.want {
			t.Errorf("around(%q, %v) = %q, want %q", c.s, c.before, got, c.want)
		}
	}
}
