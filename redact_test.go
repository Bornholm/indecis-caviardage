package main

import "testing"

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
