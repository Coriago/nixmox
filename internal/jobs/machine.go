package jobs

import (
	"errors"
)

// ErrInvalidTransition is returned when an event cannot be applied to the current state.
var ErrInvalidTransition = errors.New("invalid state transition")

// Next returns the next state given the current state and an event.
func Next(current, event string) (string, error) {
	if event == "cancel" && (current == "pending" || current == "building" || current == "applying") {
		return "cancelling", nil
	}
	if current == "cancelling" && event == "cancelled" {
		return "cancelled", nil
	}
	switch current {
	case "pending":
		if event == "start" {
			return "building", nil
		}
	case "building":
		switch event {
		case "succeeded_build":
			return "applying", nil
		case "succeeded":
			return "applied", nil
		case "failed":
			return "failed", nil
		}
	case "applying":
		switch event {
		case "succeeded":
			return "applied", nil
		case "failed":
			return "failed", nil
		}
	case "applied":
		if event == "rollback" {
			return "building", nil
		}
	case "failed":
	}

	return "", ErrInvalidTransition
}

// IsValidTransition checks if a transition is valid.
func IsValidTransition(current, event string) bool {
	_, err := Next(current, event)
	return err == nil
}
