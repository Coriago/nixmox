package deployment

import (
	"nixmox/internal/nixstore"
	"strings"
	"testing"
)

func TestClosureDiffPreservesAmbiguousOutputs(t *testing.T) {
	output := func(hash, name string) nixstore.Output {
		return nixstore.Output{Path: "/nix/store/" + strings.Repeat(hash, 32) + "-" + name, NarHash: "sha256-" + hash, NarSize: 10}
	}
	old := []nixstore.Output{output("0", "same-1.0"), output("1", "changed-1.0"), output("2", "replaced-1.0"), output("3", "removed-1.0"), output("4", "multi-1.0"), output("5", "multi-2.0"), output("6", "unversioned")}
	new := []nixstore.Output{output("0", "same-1.0"), output("7", "changed-2.0"), output("8", "replaced-1.0"), output("9", "added-1.0"), output("a", "multi-3.0"), output("b", "unversioned")}
	rows, sum, size := compareClosures(old, new)
	if sum.Added != 1 || sum.Removed != 1 || sum.VersionChanged != 1 || sum.OutputChanged != 1 || sum.Ambiguous != 2 || sum.Unchanged != 1 || *size.DeltaBytes != -10 {
		t.Fatal(sum, size)
	}
	for i, row := range rows {
		if i > 0 && rows[i-1].ID >= row.ID {
			t.Fatal("unstable ordering")
		}
		if row.Name == "multi" && (len(row.Old) != 2 || row.Kind != "ambiguous") {
			t.Fatal(row)
		}
		if row.Name == "changed" && !matchesChange(row, "version_changed", "2.0") {
			t.Fatal("version search")
		}
	}
	if matchesChange(rows[0], "added", "absent-package") {
		t.Fatal("invalid literal filter")
	}
}
