package cluster

import (
	"sync/atomic"
)

// SwitchingRecordStore writes through to local disk always and mirrors to the
// cluster KV when clustered. Local records are authoritative for this node;
// remote reads fill in other nodes without blocking local status on NATS.
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
	if r, ok, err := s.local.Latest(nodeID); err == nil && ok {
		return r, ok, nil
	}
	if remote := s.remoteOrNil(); remote != nil {
		if r, ok, err := remote.Latest(nodeID); err == nil && ok {
			return r, ok, nil
		}
	}
	return s.local.Latest(nodeID)
}

func (s *SwitchingRecordStore) History(nodeID string) ([]Record, error) {
	if records, err := s.local.History(nodeID); err == nil && len(records) > 0 {
		return records, nil
	}
	if remote := s.remoteOrNil(); remote != nil {
		if records, err := remote.History(nodeID); err == nil && len(records) > 0 {
			return records, nil
		}
	}
	return s.local.History(nodeID)
}
