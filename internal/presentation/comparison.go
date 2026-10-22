package presentation

import (
	"fmt"
	"nixmox/internal/domain"
	"strings"
)

func PackageDiff(d domain.PackageDiff) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s · version %d · %s\nBaseline: %s\nCandidate: %s\n", d.ID, d.Version, d.Scope, d.Baseline.OutputPath, d.CandidateOutput)
	if d.Stale {
		b.WriteString("STALE: baseline must be rechecked before activation.\n")
	}
	s := d.Summary
	fmt.Fprintf(&b, "%d added · %d removed · %d version changed · %d output changed · %d ambiguous · %d unchanged\n", s.Added, s.Removed, s.VersionChanged, s.OutputChanged, s.Ambiguous, s.Unchanged)
	for _, c := range d.Changes {
		fmt.Fprintf(&b, "%s  %s  %s → %s\n", c.Kind, c.Name, packageVersions(c.Old), packageVersions(c.New))
	}
	if d.NextCursor != nil {
		b.WriteString("More rows available through the comparison changes endpoint.\n")
	}
	if d.ClosureSize.BeforeBytes != nil && d.ClosureSize.AfterBytes != nil {
		fmt.Fprintf(&b, "Closure size: %s → %s (not download size)\n", formatBytes(uint64(*d.ClosureSize.BeforeBytes)), formatBytes(uint64(*d.ClosureSize.AfterBytes)))
	}
	return b.String()
}
func packageVersions(values []domain.PackageOutput) string {
	out := []string{}
	for _, v := range values {
		label := v.StorePath
		if v.Version != nil {
			label = *v.Version + " (" + v.StorePath + ")"
		}
		out = append(out, label)
	}
	if len(out) == 0 {
		return "—"
	}
	return strings.Join(out, ", ")
}
