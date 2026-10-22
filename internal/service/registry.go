// Package service owns admission and feature orchestration for every interface.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"sort"
)

type Operation struct {
	Name             string
	Scope            domain.Scope
	Capabilities     []string
	VersionResources []string
	ResultResource   string
	decode           func(json.RawMessage) (any, error)
}

// Handler implementations validate typed payloads, resolve selectors exactly once,
// and recheck evidence under exclusion. An operation without a handler cannot run.
type Handler interface {
	Validate(context.Context, domain.Command, any) error
	Resolve(context.Context, domain.Command, any) (json.RawMessage, error)
	Resources(context.Context, domain.Envelope) ([]engine.Resource, error)
	Definition() engine.TaskDefinition
}
type Registry struct{ operations map[string]Operation }

func operation[T any](name string, scope domain.Scope, result string, versions, capabilities []string) Operation {
	return Operation{Name: name, Scope: scope, ResultResource: result,
		VersionResources: versions, Capabilities: capabilities,
		decode: func(data json.RawMessage) (any, error) {
			if len(data) > domain.CommandPayloadLimit {
				return nil, fmt.Errorf("payload exceeds %d bytes", domain.CommandPayloadLimit)
			}
			data = bytes.TrimSpace(data)
			if len(data) == 0 || data[0] != '{' {
				return nil, fmt.Errorf("payload must be an object")
			}
			var value T
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				return nil, err
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				return nil, fmt.Errorf("expected one JSON object")
			}
			return value, nil
		}}
}
func (r *Registry) Lookup(name string) (Operation, bool) {
	op, ok := r.operations[name]
	return op, ok
}
func (r *Registry) Operations() []Operation {
	result := make([]Operation, 0, len(r.operations))
	for _, op := range r.operations {
		result = append(result, op)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
func (r *Registry) Decode(command domain.Command) (Operation, any, error) {
	op, ok := r.Lookup(command.Type)
	if !ok {
		return Operation{}, nil, &domain.Error{Code: "INVALID_INPUT", Message: "Unknown operation"}
	}
	if command.ID == "" || command.TargetID == "" {
		return op, nil, &domain.Error{Code: "INVALID_INPUT", Message: "Command ID and target ID are required"}
	}
	value, err := op.decode(command.Payload)
	if err != nil {
		return op, nil, &domain.Error{Code: "INVALID_INPUT", Message: err.Error()}
	}
	return op, value, nil
}
