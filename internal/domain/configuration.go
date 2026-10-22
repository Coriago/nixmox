package domain

import "encoding/json"

type Field struct {
	Name     string          `json:"name"`
	Label    string          `json:"label,omitempty"`
	Type     string          `json:"type"`
	Required bool            `json:"required"`
	Choices  []string        `json:"choices,omitempty"`
	Default  json.RawMessage `json:"default,omitempty"`
}

type Template struct {
	SchemaVersion        int                      `json:"schema_version"`
	ID                   string                   `json:"id"`
	Kind                 string                   `json:"kind"`
	Label                string                   `json:"label"`
	Fields               []Field                  `json:"fields"`
	Outputs              []TemplateOutput         `json:"outputs"`
	Configuration        *ConfigurationDescriptor `json:"configuration,omitempty"`
	PreparationTemplates *PreparationTemplates    `json:"preparation_templates,omitempty"`
	Collection           *CollectionPolicy        `json:"collection,omitempty"`
	Mode                 *string                  `json:"mode,omitempty"`
	Source               *TemplateSource          `json:"source,omitempty"`
	Assets               []TemplateAsset          `json:"assets,omitempty"`
}

type TemplateOutput struct {
	Path     string `json:"path"`
	Template string `json:"template"`
	Format   string `json:"format,omitempty"`
}

type ConfigurationDescriptor struct {
	Name           string `json:"name"`
	FlakeDirectory string `json:"flake_directory"`
	FlakeAttribute string `json:"flake_attribute"`
}

type PreparationTemplates struct {
	Hardware []string `json:"hardware"`
	Disk     []string `json:"disk"`
}

type CollectionPolicy struct {
	Method             string `json:"method"`
	IncludeFilesystems bool   `json:"include_filesystems"`
	Root               string `json:"root"`
}

type TemplateSource struct {
	Repository string `json:"repository"`
	Revision   Commit `json:"revision"`
	Template   string `json:"template"`
}

type TemplateAsset struct {
	SourcePath string `json:"source_path"`
	Path       string `json:"path"`
}

type ConfigurationCatalog struct {
	SchemaVersion  int                       `json:"schema_version"`
	Configurations []ConfigurationDescriptor `json:"configurations"`
}

type Configuration struct {
	Record
	RepositoryID         ID                    `json:"repository_id"`
	Name                 string                `json:"name"`
	FlakeDirectory       string                `json:"flake_directory"`
	FlakeAttribute       string                `json:"flake_attribute"`
	RefID                ID                    `json:"ref_id"`
	Readiness            string                `json:"readiness"`
	InspectedCommit      Commit                `json:"inspected_commit"`
	PreparationTemplates *PreparationTemplates `json:"preparation_templates,omitempty"`
}

type Draft struct {
	Record
	OwnerNodeID       ID           `json:"owner_node_id"`
	RepositoryID      ID           `json:"repository_id"`
	RepositoryVersion Version      `json:"repository_version"`
	BaseCommit        Commit       `json:"base_commit"`
	TemplateID        string       `json:"template_id"`
	Values            Values       `json:"values"`
	ConfigurationName string       `json:"configuration_name"`
	Files             []FileChange `json:"files"`
	Validation        Validation   `json:"validation"`
	AssignedHostID    *ID          `json:"assigned_host_id"`
	ExpectedVersions  Versions     `json:"expected_versions"`
}

type FileChange struct {
	Path   string `json:"path"`
	Change string `json:"change"`
}

type DraftBuffer struct {
	Path              string  `json:"path"`
	GeneratedBaseline string  `json:"generated_baseline"`
	Content           string  `json:"content"`
	ManuallyEdited    bool    `json:"manually_edited"`
	Regenerated       *string `json:"regenerated"`
}

type Validation struct {
	ID           ID           `json:"id"`
	State        string       `json:"state"`
	DraftVersion *Version     `json:"draft_version"`
	Commit       *Commit      `json:"commit"`
	Missing      []string     `json:"missing"`
	Errors       []FieldError `json:"errors"`
}

type GeneratedOutput struct {
	Path             string `json:"path"`
	TemplateRevision Commit `json:"template_revision"`
	GeneratedDigest  Digest `json:"generated_digest"`
}
