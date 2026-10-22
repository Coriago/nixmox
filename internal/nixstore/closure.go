package nixstore

import (
	"context"
	"encoding/json"
	"nixmox/internal/domain"
	"nixmox/internal/process"
	"sort"
	"strings"
	"time"
)

// Closure queries realized store objects. Evaluation or a builder's current
// system cannot substitute for either of the requested output identities.
func (n Nix) Closure(ctx context.Context, path string) ([]Output, error) {
	if !PathPattern.MatchString(path) {
		return nil, domain.Err("OUTPUT_INVALID", "Invalid closure root.", 422)
	}
	r, err := process.Run(ctx, process.Options{Timeout: time.Minute, MaxOutput: 16 << 20}, "nix", "path-info", "--recursive", "--json", path)
	var info map[string]struct {
		NarHash string `json:"narHash"`
		NarSize *int64 `json:"narSize"`
		Deriver string `json:"deriver"`
	}
	if err != nil || json.Unmarshal(r.Stdout, &info) != nil || len(info) == 0 || len(info) > 20000 {
		return nil, domain.Err("COMPARISON_UNAVAILABLE", "Realized closure metadata is unavailable or exceeds the 20,000-path bound.", 503)
	}
	if _, ok := info[path]; !ok {
		return nil, domain.Err("OUTPUT_INVALID", "Closure omits the requested root.", 409)
	}
	outputs := make([]Output, 0, len(info))
	for path, v := range info {
		if !PathPattern.MatchString(path) || len(path) > 4096 || !strings.HasPrefix(v.NarHash, "sha256-") || v.NarSize == nil || *v.NarSize < 0 {
			return nil, domain.Err("OUTPUT_INVALID", "Closure contains invalid store metadata.", 409)
		}
		outputs = append(outputs, Output{Path: path, NarHash: v.NarHash, NarSize: *v.NarSize, Derivation: v.Deriver})
	}
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Path < outputs[j].Path })
	return outputs, nil
}

func (n Nix) Retain(ctx context.Context, output, root string) error {
	if !PathPattern.MatchString(output) {
		return domain.Err("OUTPUT_INVALID", "Invalid retained output.", 422)
	}
	_, err := process.Run(ctx, process.Options{Timeout: 15 * time.Second, MaxOutput: 64 << 10}, "nix-store", "--add-root", root, "--indirect", "--realise", output)
	if err != nil {
		return domain.Err("OUTPUT_UNAVAILABLE", "Cannot retain the verified output.", 503)
	}
	return nil
}
