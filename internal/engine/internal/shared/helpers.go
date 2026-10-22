package shared

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"

	"nixmox/internal/domain"
)

func Clone[T any](value T) T {
	data, _ := json.Marshal(value)
	var copy T
	_ = json.Unmarshal(data, &copy)
	return copy
}

// Token is injective and never admits wildcard, dot or whitespace tokens.
func Token(id domain.ID) string { return hex.EncodeToString([]byte(id)) }

func Fingerprint(cert []byte) domain.Digest {
	sum := sha256.Sum256(cert)
	return domain.Digest(hex.EncodeToString(sum[:]))
}

var commitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func ValidCommit(commit domain.Commit) bool { return commitPattern.MatchString(string(commit)) }

func Ptr[T any](v T) *T { return &v }

func Rate(old, current uint64, elapsed float64, known bool) *float64 {
	if !known || elapsed <= 0 || current < old {
		return nil
	}
	return Ptr(float64(current-old) / elapsed)
}

func ValidBundle(b domain.Bundle) bool {
	raw, e := hex.DecodeString(string(b.SHA256))
	return e == nil && len(raw) == sha256.Size && b.Bytes > 0 && b.ObjectKey != ""
}

func ID(prefix string, id domain.ID) domain.ID {
	sum := sha256.Sum256([]byte(id))
	return domain.ID(prefix + hex.EncodeToString(sum[:]))
}

func Filename(id domain.ID) string { return string(ID("", id)) }
