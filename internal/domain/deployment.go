package domain

import "time"

type Candidate struct {
	SchemaVersion   int                  `json:"schema_version"`
	ID              string               `json:"id"`
	Version         int64                `json:"version"`
	Source          Source               `json:"source"`
	BuilderID       string               `json:"builder_id"`
	System          string               `json:"system"`
	State           string               `json:"state"`
	OutputPath      string               `json:"output_path"`
	NarHash         string               `json:"nar_hash"`
	InputLockDigest string               `json:"input_lock_digest"`
	DerivationPath  string               `json:"derivation_path"`
	TaskID          string               `json:"task_id"`
	CreatedAt       time.Time            `json:"created_at"`
	Comparison      *CandidateComparison `json:"comparison"`
}
type CandidateComparison struct {
	DiffID     string `json:"diff_id"`
	HostID     string `json:"host_id"`
	Generation int    `json:"generation"`
	Stale      bool   `json:"stale"`
}
