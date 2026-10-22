package storage

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"

	"nixmox/internal/domain"
)

const LogRetention = 4 << 20
const MaxLogAppend = 64 << 10

type Log struct {
	Start int
	Data  []byte
}

func (l Log) End() int { return l.Start + len(l.Data) }

// ReadLog returns a fixed, bounded snapshot. The service serializes writers.
func (s *Store) ReadLog(id string) (Log, error) {
	if !domain.SafeID.MatchString(id) {
		return Log{}, errors.New("invalid log ID")
	}
	f, err := os.Open(filepath.Join(s.Root, "logs", id+".log"))
	if errors.Is(err, os.ErrNotExist) {
		return Log{Data: []byte{}}, nil
	}
	if err != nil {
		return Log{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, LogRetention+9))
	if err != nil {
		return Log{}, err
	}
	if len(b) < 8 || len(b) > LogRetention+8 {
		return Log{}, errors.New("corrupt log")
	}
	start := binary.BigEndian.Uint64(b[:8])
	if start > uint64(^uint(0)>>1)-uint64(len(b)) {
		return Log{}, errors.New("log offset overflow")
	}
	return Log{Start: int(start), Data: b[8:]}, nil
}
func (s *Store) AppendLog(id string, data []byte) (int, error) {
	if len(data) > MaxLogAppend {
		return 0, errors.New("log chunk exceeds limit")
	}
	l, err := s.ReadLog(id)
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return l.End(), nil
	}
	l.Data = append(l.Data, data...)
	if excess := len(l.Data) - LogRetention; excess > 0 {
		l.Start += excess
		l.Data = l.Data[excess:]
	}
	b := make([]byte, 8+len(l.Data))
	binary.BigEndian.PutUint64(b, uint64(l.Start))
	copy(b[8:], l.Data)
	return l.End(), s.WriteBytes("logs/"+id+".log", b)
}
