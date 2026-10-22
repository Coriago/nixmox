package domain

// Effective settings may include runtime file paths, but never file contents.
type Settings struct {
	Version   Version                 `json:"version"`
	Values    Values                  `json:"values"`
	Ownership map[string]SettingOwner `json:"ownership"`
}
type SettingOwner struct {
	Source   string `json:"source"`
	NodeIDs  []ID   `json:"node_ids"`
	ReadOnly bool   `json:"read_only"`
}
type ResourceUpdate struct {
	Type     string  `json:"type"`
	Resource string  `json:"resource"`
	ID       ID      `json:"id"`
	Version  Version `json:"version"`
	Observed
}
type ProgressEvent struct {
	TaskID  ID      `json:"task_id"`
	Version Version `json:"version"`
	Observed
	Progress TaskProgress `json:"progress"`
}
