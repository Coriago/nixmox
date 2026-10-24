package domain

// RecipeVersion selects history, never a moving latest version.
type RecipeVersion struct {
	ID      ID      `json:"id"`
	Version Version `json:"version"`
}

// RecipePin is retained by tasks, deployments, publications and history reads.
type RecipePin struct {
	RecipeVersion
	Digest        Digest `json:"digest"`
	ContentDigest Digest `json:"content_digest"`
}
