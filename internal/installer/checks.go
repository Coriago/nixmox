package installer

import (
	"context"
	"fmt"
	"sort"

	"nixmox/internal/installer/installers"
	"nixmox/internal/installer/prepare"
)

type Candidate struct {
	Installer      installers.Definition `json:"installer"`
	Prepare        prepare.Definition    `json:"prepare"`
	InstallerCheck installers.Check      `json:"installerCheck"`
	PrepareCheck   installers.Check      `json:"prepareCheck"`
	Compatible     bool                  `json:"compatible"`
	Preference     int                   `json:"preference"`
}

type Checks struct {
	Target           installers.Target `json:"target"`
	Candidates       []Candidate       `json:"candidates"`
	Selected         *Candidate        `json:"selected,omitempty"`
	ExecutionReady   bool              `json:"executionReady"`
	ExecutionBlocker string            `json:"executionBlocker,omitempty"`
}

// Select ranks compatible installer/prepare pairs. Explicit IDs filter choices;
// they never override hard compatibility failures.
func Select(target installers.Target, definitions []installers.Definition, installerID, prepareID string) ([]Candidate, *Candidate) {
	candidates := make([]Candidate, 0, len(definitions))
	method := prepare.Kexec{}
	for _, definition := range definitions {
		if installerID != "" && definition.ID != installerID {
			continue
		}
		if prepareID != "" && method.Definition().ID != prepareID {
			continue
		}
		profile, err := installers.Lookup(definition.Kind)
		check := installers.Check{}
		if err != nil {
			check.Reason = err.Error()
		} else {
			check = profile.Check(target, definition)
		}
		candidate := Candidate{Installer: definition, Prepare: method.Definition(), InstallerCheck: check, PrepareCheck: method.Check(target, definition)}
		candidate.Compatible = candidate.InstallerCheck.Compatible && candidate.PrepareCheck.Compatible
		candidate.Preference = candidate.InstallerCheck.Preference + candidate.PrepareCheck.Preference
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Compatible != candidates[j].Compatible {
			return candidates[i].Compatible
		}
		if candidates[i].Preference != candidates[j].Preference {
			return candidates[i].Preference > candidates[j].Preference
		}
		return candidates[i].Installer.ID < candidates[j].Installer.ID
	})
	if len(candidates) > 0 && candidates[0].Compatible {
		return candidates, &candidates[0]
	}
	return candidates, nil
}

func (s *Service) Checks(ctx context.Context, id, installerID, prepareID string) (Checks, error) {
	target, err := s.target(ctx, id)
	if err != nil {
		return Checks{}, err
	}
	definitions := []installers.Definition{}
	if installerID != "" {
		record, err := s.Definitions.Get(ctx, installerID)
		if err != nil {
			return Checks{}, err
		}
		definitions = append(definitions, record.Value)
	} else {
		cursor := ""
		for {
			records, next, err := s.Definitions.List(ctx, cursor, 500)
			if err != nil {
				return Checks{}, err
			}
			for _, record := range records {
				definitions = append(definitions, record.Value)
			}
			if len(definitions) > 10000 {
				return Checks{}, fmt.Errorf("%w: too many installers; select an explicit ID", ErrInvalid)
			}
			if next == "" {
				break
			}
			cursor = next
		}
	}
	candidates, selected := Select(target, definitions, installerID, prepareID)
	result := Checks{Target: target, Candidates: candidates, Selected: selected}
	if err = s.ready(ctx, target); err != nil {
		result.ExecutionBlocker = err.Error()
	} else {
		result.ExecutionReady = true
	}
	return result, nil
}

func (s *Service) ready(ctx context.Context, target installers.Target) error {
	if s.Transition == nil {
		return fmt.Errorf("%w: client-only startup, persistent task recovery and safe demotion/handoff are not wired", ErrUnavailable)
	}
	return s.Transition.Ready(ctx, target)
}
