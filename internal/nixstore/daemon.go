package nixstore

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"nixmox/internal/domain"
	"regexp"
	"strings"
	"time"
)

// A read-only worker-protocol adapter, pinned to protocol 1.34 as implemented by
// Nix 2.34.9. QueryPathFromHashPart uses Nix's live index; there is no per-request
// store scan. Keeping this connection open holds its daemon temporary GC roots.
type Daemon struct{ Socket string }
type StoreMetadata struct {
	Path       string
	NarHash    []byte
	NarSize    uint64
	References []string
	Deriver    string
	Signatures []string
	CA         string
}
type StoreLease struct {
	Metadata   StoreMetadata
	connection net.Conn
	stop       func() bool
}

func (l *StoreLease) Close() error { l.stop(); return l.connection.Close() }

type wire struct {
	net.Conn
	err error
}

func (w *wire) number() uint64 {
	var b [8]byte
	if w.err == nil {
		_, w.err = io.ReadFull(w.Conn, b[:])
	}
	return binary.LittleEndian.Uint64(b[:])
}
func (w *wire) send(n uint64) {
	if w.err == nil {
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], n)
		_, w.err = w.Conn.Write(b[:])
	}
}
func (w *wire) text() string {
	length := w.number()
	if length > 1<<20 {
		w.err = domain.Err("STORE_PROTOCOL", "Store metadata exceeds its bound.", 503)
		return ""
	}
	data := make([]byte, (length+7)&^7)
	if w.err == nil {
		_, w.err = io.ReadFull(w.Conn, data)
	}
	return string(data[:length])
}
func (w *wire) writeText(s string) {
	w.send(uint64(len(s)))
	if w.err != nil {
		return
	}
	data := make([]byte, (len(s)+7)&^7)
	copy(data, s)
	_, w.err = w.Conn.Write(data)
}
func (w *wire) texts(limit uint64) []string {
	count := w.number()
	if count > limit {
		w.err = domain.Err("STORE_PROTOCOL", "Store metadata item count exceeds its bound.", 503)
		return nil
	}
	out := make([]string, 0, count)
	for i := uint64(0); i < count && w.err == nil; i++ {
		out = append(out, w.text())
	}
	return out
}
func (w *wire) fields() {
	count := w.number()
	if count > 64 {
		w.err = domain.Err("STORE_PROTOCOL", "Store event exceeds its bound.", 503)
		return
	}
	for i := uint64(0); i < count && w.err == nil; i++ {
		switch w.number() {
		case 0:
			w.number()
		case 1:
			w.text()
		default:
			w.err = domain.Err("STORE_PROTOCOL", "Unknown store event field.", 503)
		}
	}
}
func (w *wire) stderr() error {
	for count := 0; count < 1024 && w.err == nil; count++ {
		switch w.number() {
		case 0x616c7473:
			return w.err
		case 0x6f6c6d67:
			w.text() // discard raw diagnostics; they never enter tasks
		case 0x53545254:
			w.number()
			w.number()
			w.number()
			w.text()
			w.fields()
			w.number()
		case 0x53544f50:
			w.number()
		case 0x52534c54:
			w.number()
			w.number()
			w.fields()
		default:
			return domain.Err("STORE_UNAVAILABLE", "Nix daemon rejected the bounded metadata request.", 503)
		}
	}
	return domain.Err("STORE_UNAVAILABLE", "Nix daemon metadata protocol is unavailable.", 503)
}

var StoreHashPattern = regexp.MustCompile(`^[0-9abcdfghijklmnpqrsvwxyz]{32}$`)

func (d Daemon) Lease(ctx context.Context, hash string) (*StoreLease, error) {
	if !StoreHashPattern.MatchString(hash) {
		return nil, domain.Err("INVALID_PATH", "Invalid store hash.", 422)
	}
	socket := d.Socket
	if socket == "" {
		socket = "/nix/var/nix/daemon-socket/socket"
	}
	connection, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, domain.Err("STORE_UNAVAILABLE", "Local Nix daemon is unavailable.", 503)
	}
	success := false
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer func() {
		if !success {
			stop()
			connection.Close()
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = connection.SetDeadline(deadline)
	w := &wire{Conn: connection}
	w.send(0x6e697863)
	w.send(0x122)
	if w.number() != 0x6478696f {
		return nil, domain.Err("STORE_PROTOCOL", "Nix worker protocol handshake failed.", 503)
	}
	version := w.number()
	if version>>8 != 1 || version&255 < 34 {
		return nil, domain.Err("STORE_PROTOCOL", "Nix worker protocol 1.34 or later is required.", 503)
	}
	w.send(0)
	w.send(0)
	w.text() // daemon version, with no optional trust/features in 1.34
	if err = w.stderr(); err != nil {
		return nil, err
	}
	w.send(29)
	w.writeText(hash)
	if err = w.stderr(); err != nil {
		return nil, err
	}
	path := w.text()
	if w.err != nil {
		return nil, domain.Err("STORE_UNAVAILABLE", "Store hash lookup failed.", 503)
	}
	if path == "" {
		return nil, domain.Err("NOT_FOUND", "Store output is not present on this node.", 404)
	}
	if !PathPattern.MatchString(path) || !strings.HasPrefix(path, "/nix/store/"+hash+"-") {
		return nil, domain.Err("OUTPUT_INVALID", "Store lookup returned another output identity.", 409)
	}
	w.send(11)
	w.writeText(path)
	if err = w.stderr(); err != nil {
		return nil, err
	}
	if w.number() != 1 || w.err != nil {
		return nil, domain.Err("STORE_UNAVAILABLE", "Cannot acquire a serving GC root.", 503)
	}
	w.send(26)
	w.writeText(path)
	if err = w.stderr(); err != nil {
		return nil, err
	}
	if w.number() != 1 {
		return nil, domain.Err("NOT_FOUND", "Store output disappeared before it could be pinned.", 404)
	}
	metadata := StoreMetadata{Path: path, Deriver: w.text()}
	hashText := w.text()
	metadata.NarHash, err = hex.DecodeString(hashText)
	if err != nil || len(metadata.NarHash) != 32 {
		return nil, domain.Err("OUTPUT_INVALID", "Invalid NAR hash from the store.", 409)
	}
	metadata.References = w.texts(20000)
	w.number()
	metadata.NarSize = w.number()
	w.number()
	metadata.Signatures = w.texts(256)
	metadata.CA = w.text()
	if w.err != nil || metadata.NarSize == 0 {
		return nil, domain.Err("STORE_UNAVAILABLE", "Incomplete store metadata.", 503)
	}
	for _, ref := range metadata.References {
		if !PathPattern.MatchString(ref) {
			return nil, domain.Err("OUTPUT_INVALID", "Invalid store reference.", 409)
		}
	}
	_ = connection.SetDeadline(time.Time{})
	success = true
	return &StoreLease{Metadata: metadata, connection: connection, stop: stop}, nil
}
