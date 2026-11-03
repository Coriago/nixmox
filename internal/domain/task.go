// Package domain defines wire records shared by daemon and interfaces.
package domain

import (
	"encoding/json"
	"time"
)

type TaskStatus string

const (
	Queued       TaskStatus = "queued"
	Running      TaskStatus = "running"
	ManualReview TaskStatus = "manual-review"
	Succeeded    TaskStatus = "succeeded"
	Failed       TaskStatus = "failed"
	Cancelled    TaskStatus = "cancelled"
	Unknown      TaskStatus = "unknown"
)

func (s TaskStatus) Terminal() bool {
	return s == Succeeded || s == Failed || s == Cancelled || s == Unknown
}

type Task struct {
	ID          string                     `json:"id"`
	Type        string                     `json:"tasktype"`
	HostID      string                     `json:"hostId"`
	Status      TaskStatus                 `json:"status"`
	CreatedAt   time.Time                  `json:"createdAt"`
	StartedAt   *time.Time                 `json:"startedAt,omitempty"`
	FinishedAt  *time.Time                 `json:"finishedAt,omitempty"`
	Links       TaskLinks                  `json:"links"`
	Concurrency TaskConcurrency            `json:"concurrency"`
	Inputs      json.RawMessage            `json:"inputs"`
	Context     map[string]json.RawMessage `json:"context"`
	Trace       map[string]string          `json:"trace,omitempty"`
	Review      *Review                    `json:"review,omitempty"`
	Result      json.RawMessage            `json:"result,omitempty"`
	Error       *TaskError                 `json:"error,omitempty"`
}

type TaskLinks struct {
	ParentTaskID          string      `json:"parentTaskId,omitempty"`
	RetryOfTaskID         string      `json:"retryOfTaskId,omitempty"`
	AfterSuccessOfTaskIDs []string    `json:"afterSuccessOfTaskIds"`
	ChildTasks            []ChildTask `json:"childTasks"`
	Outputs               []OutputRef `json:"outputs"`
}

type ChildTask struct {
	TaskID string `json:"taskId"`
	Role   string `json:"role"`
}

type OutputRef struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

type TaskConcurrency struct {
	ExclusiveResources []string `json:"exclusiveResources"`
}

type Review struct {
	OutputID   string     `json:"outputId"`
	ApprovedAt *time.Time `json:"approvedAt"`
}

type TaskError struct {
	Message string `json:"message"`
}

type TaskOutput struct {
	ID      string          `json:"id"`
	TaskID  string          `json:"taskId"`
	Kind    string          `json:"kind"`
	Content json.RawMessage `json:"content"`
}
