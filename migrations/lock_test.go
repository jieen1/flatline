package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// A migration is append-only once any database has recorded it. An edited file
// is never re-run, so the change reaches only the databases created afterwards
// and every older one is left with a schema the code no longer expects — the
// daemon then fails on a column that is missing for half the installs.
//
// releasedMigrations locks the content of every published file. Changing one
// turns this test red; the fix is always a new migration file, never an edit.
var releasedMigrations = map[string]string{
	"001_initial":                    "2bf355b2db1c703e9464a055360e6d1a1fc615fb41dbf8f76cbe902d41e4ecac",
	"002_p3_asset_version_identity":  "0355490f0debecbf034751a0cb1f9878809d3511e9af3fc3ae7a2db0a7bde369",
	"003_reference_unknown_nullable": "0cf65048c67b2200b3634347ea00798384f70464ee92ce75be7e5571879f54e6",
	"004_native_session_transcript":  "5a518fd33a24665ec55463015f4938b1abecceb7c1e2b05a3746a17555b53be8",
	"005_friction_records":           "40f54510e516f2845da63eb10ef17cc3af39915476e3e4922bacdf8658d4e749",
	"006_session_management":         "03a65a0215a43b1471e93d4914232dc68db1c99162ba5ebac97a8ebbb0a607d3",
	"007_friction_categories":        "54651cd974a45df60ee10cc456772beccced18fc8b874396a083ec9ee013bbe4",
	"008_session_hierarchy":          "a83959ff8fa1af305f88ef40500e133177a352e8037a5d2b50f1c158321ffea2",
	"009_friction_signature":         "6869a0fa973a354c67faba2636840e8b494b22fe350f7fbc40834fc81bc32955",
	"010_event_pairs":                "ee50dfa29d4811bf8b419d6297406cafa711696a839aa423f279aeb5ab558ce1",
	"011_session_usage":              "0d24a8b1d1d303ecb4db7deabb89e9ab8c5a64696d9ceb5b6a26790459f0c5d7",
	"012_open_sources":               "07143c3a5502c6aad9fc66d9c83ec8375b9057f424ad8994b64725a0591e7e94",
	"013_projection_version":         "62436e9e3fbf021d3aa2d5ef136ebf34ddc84a9e75db1a45d4b50c994d82cc54",
	"014_asset_evidence_supersede":   "2143d27fddb3829390996cc73d278073c1743ed04406ef7a8ac2cc7477bf4d69",
	"015_session_commands_ordinal":   "644536df031bc0b3eaaa90b5ff2f1522c9262726bdd31c740c79ba48615d3cea",
	"016_sources":                    "e69ecfccdfcf4a4b73a05ec65dc13d850b2cf1362926bda5dff4ed433caf9003",
	"017_meta":                       "0d61c43d339ecb2658fc68a90b92cd31625e18444ad602e9dd501c0f4f3f2ac9",
	"019_session_span_repair":        "577cbe3c61b49f67ecbe3e1221ae7cc87a8b87d23cc8a3366fa744a1c3f01ea4",
	"018_session_project_key":        "46d22dd6dd46c8500c16bc4d392b01cf9af0b136b8ff7772f952afbc9258102c",
	"020_asset_friction_links":       "d8fc21e9893553be6355e2ee1c435eddb7f63f23c7ff2bd3f87ffb380d64a11c",
	"021_friction_category_rule_en":  "23c79f00c191e4810994e667b83542664eed24b3212dc836a3cf5003b00b99cf",
	"022_signature_watches":          "a27e49e8614617d24e9d689d3ce194b8e1999c67c7805ba8e0854761ddf48202",
}

func TestReleasedMigrationsAreNeverEdited(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	seen := make(map[string]struct{}, len(all))
	for _, m := range all {
		seen[m.Name] = struct{}{}
		want, locked := releasedMigrations[m.Name]
		if !locked {
			t.Errorf("migration %q is not locked; add its sha256 to releasedMigrations", m.Name)
			continue
		}
		sum := sha256.Sum256([]byte(m.SQL))
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("migration %q was edited after release\n  have %s\n  want %s\n"+
				"a published migration is append-only: add a new file instead", m.Name, got, want)
		}
	}
	for name := range releasedMigrations {
		if _, ok := seen[name]; !ok {
			t.Errorf("locked migration %q is missing from the embedded set", name)
		}
	}
}
