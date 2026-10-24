package domain

type ConfigurationDescriptor struct {
	Name           string `json:"name"`
	FlakeDirectory string `json:"flake_directory"`
	FlakeAttribute string `json:"flake_attribute"`
}

type PreparationTemplates struct {
	Hardware []string `json:"hardware"`
	Disk     []string `json:"disk"`
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
	RefID                ID                    `json:"ref_id,omitempty"`
	RecipeID             *ID                   `json:"recipe_id,omitempty"`
	Readiness            string                `json:"readiness"`
	InspectedCommit      Commit                `json:"inspected_commit"`
	PreparationTemplates *PreparationTemplates `json:"preparation_templates,omitempty"`
}

type Validation struct {
	ID           ID           `json:"id"`
	State        string       `json:"state"`
	DraftVersion *Version     `json:"draft_version"`
	Commit       *Commit      `json:"commit"`
	Recipe       *RecipePin   `json:"recipe,omitempty"`
	Missing      []string     `json:"missing"`
	Errors       []FieldError `json:"errors"`
}
