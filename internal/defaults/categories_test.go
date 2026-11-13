package defaults

import (
	"strings"
	"testing"
)

func TestDefaultsAreConsistent(t *testing.T) {
	for name, list := range map[string][]Category{"personal": Personal, "business": Business} {
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
