package jobs

import (
	"testing"
	"testing/quick"
)

func TestMachineTransitions(t *testing.T) {
	tests := []struct {
		current string
		event   string
		want    string
		wantErr bool
	}{
		{"pending", "start", "building", false},
		{"building", "succeeded_build", "applying", false},
		{"building", "succeeded", "applied", false},
		{"building", "failed", "failed", false},
		{"applying", "succeeded", "applied", false},
		{"applying", "failed", "failed", false},
		{"applied", "rollback", "building", false},

		{"pending", "succeeded", "", true},
		{"pending", "failed", "", true},
		{"building", "start", "", true},
		{"applying", "start", "", true},
		{"applied", "succeeded", "", true},
		{"failed", "start", "", true},
		{"failed", "succeeded", "", true},
		{"failed", "failed", "", true},
		{"unknown", "start", "", true},
		{"pending", "unknown", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.current+"_"+tt.event, func(t *testing.T) {
			got, err := Next(tt.current, tt.event)
			if (err != nil) != tt.wantErr {
				t.Errorf("Next() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("Next() = %v, want %v", got, tt.want)
			}

			isValid := IsValidTransition(tt.current, tt.event)
			if isValid == tt.wantErr {
				t.Errorf("IsValidTransition() = %v, want %v", isValid, !tt.wantErr)
			}
		})
	}
}

func TestMachineFuzz(t *testing.T) {
	f := func(current, event string) bool {
		valid := false
		switch current {
		case "pending":
			if event == "start" {
				valid = true
			}
		case "building":
			if event == "succeeded_build" || event == "succeeded" || event == "failed" {
				valid = true
			}
		case "applying":
			if event == "succeeded" || event == "failed" {
				valid = true
			}
		case "applied":
			if event == "rollback" {
				valid = true
			}
		}

		_, err := Next(current, event)
		return (err == nil) == valid
	}

	if err := quick.Check(f, nil); err != nil {
		t.Error(err)
	}
}
