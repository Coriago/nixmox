package jobs

// ObservableStore notifies only after persistence succeeds. Callbacks must be
// nonblocking; cluster publication runs independently of command execution.
type ObservableStore struct {
	Store
	Changed     func()
	LogAppended func(string, LogChunk)
}

func (s ObservableStore) notify(err error) error {
	if err == nil && s.Changed != nil {
		s.Changed()
	}
	return err
}
func (s ObservableStore) Create(j Job) error { return s.notify(s.Store.Create(j)) }
func (s ObservableStore) Update(j Job) error { return s.notify(s.Store.Update(j)) }
func (s ObservableStore) AppendLog(id, line string) error {
	if store, ok := s.Store.(interface {
		AppendLogChunk(string, string) (LogChunk, error)
	}); ok {
		chunk, err := store.AppendLogChunk(id, line)
		if err == nil && s.LogAppended != nil {
			s.LogAppended(id, chunk)
			return nil
		}
		return s.notify(err)
	}
	return s.notify(s.Store.AppendLog(id, line))
}

// ActiveDeployment excludes logs, command arguments and potentially large previews.
func ActiveDeployment(store Store) (*Job, error) {
	list, err := store.List()
	if err != nil {
		return nil, err
	}
	var active *Job
	for _, j := range list {
		if (j.Kind == "apply" || j.Kind == "rollback") && (j.State == "pending" || j.State == "building" || j.State == "applying" || j.State == "cancelling") {
			if active == nil || j.State == "building" {
				active = &Job{ID: j.ID, Kind: j.Kind, State: j.State, Phase: j.Phase, Progress: j.Progress, Total: j.Total}
			}
		}
	}
	return active, nil
}
