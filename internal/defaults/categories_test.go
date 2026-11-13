package defaults

import (
	"strings"
	"testing"
)

func TestDefaultsAreConsistent(t *testing.T) {
	for name, list := range map[string][]Category{"personal": Personal, "business": Business, "personal-en": PersonalEN, "business-en": BusinessEN} {
		seen := map[string]bool{}
		for _, c := range list {
			if c.Kind != "INCOME" && c.Kind != "EXPENSE" {
				t.Errorf("%s: %q has kind %q", name, c.Name, c.Kind)
			}
			if !ValidIcon(c.Icon) {
				t.Errorf("%s: %q uses icon %q, which isn't in Icons", name, c.Name, c.Icon)
			}
			key := c.Kind + "/" + strings.ToLower(c.Name)
			if seen[key] {
				t.Errorf("%s: duplicate %s", name, key)
			}
			seen[key] = true
			if len([]rune(c.Name)) > 60 {
				t.Errorf("%s: %q is too long", name, c.Name)
			}
		}
	}
	icons := map[string]bool{}
	for _, i := range Icons {
		if icons[i] {
			t.Errorf("duplicate icon %q", i)
		}
		icons[i] = true
	}
}

// The English lists mirror the Spanish ones, so both languages get the same
// kinds and icons in the same order.
func TestEnglishListsMirrorSpanish(t *testing.T) {
	for _, pair := range [][2][]Category{{Personal, PersonalEN}, {Business, BusinessEN}} {
		es, en := pair[0], pair[1]
		if len(es) != len(en) {
			t.Fatalf("lengths differ: %d es, %d en", len(es), len(en))
		}
		for i := range es {
			if es[i].Kind != en[i].Kind || es[i].Icon != en[i].Icon {
				t.Errorf("%q and %q don't match", es[i].Name, en[i].Name)
			}
		}
	}
	if For("BUSINESS", "en")[0].Name != "Sales" || For("PERSONAL", "fr")[0].Name != "Salario" {
		t.Error("For picks the wrong list")
	}
}
