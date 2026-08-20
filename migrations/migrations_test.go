package migrations

import (
	"testing"
)

func TestAllOrderedAndComplete(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no migrations found")
	}
	for i, m := range all {
		if m.Version <= 0 {
			t.Errorf("migration %d has non-positive version %d", i, m.Version)
		}
		if m.Name == "" {
			t.Errorf("migration %d has empty name", i)
		}
		if m.SQL == "" {
			t.Errorf("migration %d has empty SQL", i)
		}
		if i > 0 && m.Version <= all[i-1].Version {
			t.Errorf("migrations not in ascending order at index %d: %d after %d", i, m.Version, all[i-1].Version)
		}
	}
	// The initial migration must be version 1 and named 001_initial.
	if all[0].Version != 1 || all[0].Name != "001_initial" {
		t.Errorf("first migration = (%d, %q), want (1, 001_initial)", all[0].Version, all[0].Name)
	}
}
