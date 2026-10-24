package domain

import "time"

// Recipe is one accepted, immutable version of a stable recipe ID. Version starts
// at 1 and advances on every accepted edit; latest reads return only the head.
// Persistence and generation are future work, not implemented by these contracts.
type Recipe struct {
	Record
	RepositoryID    ID               `json:"repository_id"`
	ConfigurationID ID               `json:"configuration_id"`
	CommandID       ID               `json:"command_id"`
	CreatedAt       time.Time        `json:"created_at"`
	Digest          Digest           `json:"digest"`
	Definition      RecipeDefinition `json:"definition"`
}

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

// RecipeDefinition is the complete digest-covered reproducible definition.
// Only public inputs belong here; never private keys or decrypted secrets.
type RecipeDefinition struct {
	Upstream       GitSource         `json:"upstream"`
	Templates      []TemplateSource  `json:"templates"`
	Generator      RecipeTool        `json:"generator"`
	SOPSUpdater    RecipeTool        `json:"sops_updater"`
	SchemaVersions map[string]int    `json:"schema_versions"`
	Inputs         Values            `json:"inputs"`
	Observations   Values            `json:"observations"`
	OwnedFiles     []GeneratedOutput `json:"owned_files"`
	Overrides      []RecipeOverride  `json:"overrides"`
	SOPSInputs     Values            `json:"sops_inputs"`
	ContentDigest  Digest            `json:"content_digest"`
}

type GitSource struct {
	RepositoryID   ID     `json:"repository_id"`
	Commit         Commit `json:"commit"`
	FlakeDirectory string `json:"flake_directory"`
	FlakeAttribute string `json:"flake_attribute"`
}

// RecipeTool pins retained implementation bytes, not just a mutable version label.
type RecipeTool struct {
	Version string `json:"version"`
	Digest  Digest `json:"digest"`
}

// RecipeOverride replaces a file's bytes/mode, or explicitly deletes its path.
// Deleted entries have no content/mode. Paths must remain within the checkout.
type RecipeOverride struct {
	Path    string `json:"path"`
	Content []byte `json:"content_base64,omitempty"`
	Mode    uint32 `json:"mode,omitempty"`
	Delete  bool   `json:"delete"`
}
