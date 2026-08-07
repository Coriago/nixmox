package cluster

import (
	"sync/atomic"
)

// SwitchingRecordStore writes through to local disk always and mirrors to the
// cluster KV when clustered. Reads prefer the remote view when clustered so
// the portal sees the shared state. M4's apply code is untouched: it only
// ever sees the RecordStore interface.
type SwitchingRecordStore struct {
	local  RecordStore
	remote atomic.Value // RecordStore (KV-backed), set on cluster arm
}

func NewSwitchingRecordStore(local RecordStore) *SwitchingRecordStore {
	return &SwitchingRecordStore{local: local}
}

func (s *SwitchingRecordStore) SetRemote(r RecordStore) {
	s.remote.Store(&r)
}

func (s *SwitchingRecordStore) remoteOrNil() RecordStore {
	if v := s.remote.Load(); v != nil {
		return *(v.(*RecordStore))
	}
	return nil
}

func (s *SwitchingRecordStore) Record(nodeID string, r Record) error {
	if err := s.local.Record(nodeID, r); err != nil {
		return err
	}
	if remote := s.remoteOrNil(); remote != nil {
		return remote.Record(nodeID, r)
	}
	return nil
}

func (s *SwitchingRecordStore) Latest(nodeID string) (Record, bool, error) {
	if remote := s.remoteOrNil(); remote != nil {
		return remote.Latest(nodeID)
	}
	return s.local.Latest(nodeID)
}

func (s *SwitchingRecordStore) History(nodeID string) ([]Record, error) {
	if remote := s.remoteOrNil(); remote != nil {
		return remote.History(nodeID)
	}
	return s.local.History(nodeID)
}
