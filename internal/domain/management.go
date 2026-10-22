package domain

import "time"

type ManagedEvidence struct {
	HostID       string    `json:"host_id"`
	Epoch        int       `json:"membership_epoch"`
	CandidateID  string    `json:"candidate_id"`
	DeploymentID string    `json:"deployment_id"`
	Source       Source    `json:"source"`
	OutputPath   string    `json:"output_path"`
	NarHash      string    `json:"nar_hash"`
	VerifiedAt   time.Time `json:"verified_at"`
	Environment  string    `json:"environment"`
}
