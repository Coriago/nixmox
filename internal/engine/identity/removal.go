package identity

import (
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"nixmox/internal/domain"
	"nixmox/internal/engine/internal/shared"
)

type retirement struct {
	Notice   domain.SignedRemoval `json:"notice"`
	Trust    domain.ClusterTrust  `json:"trust"`
	Complete bool                 `json:"complete"`
}

func (l *Local) SignRemoval(record domain.Removal) (domain.SignedRemoval, error) {
	var issuer issuerState
	if err := l.load("pki/issuer/genesis.json", &issuer); err != nil {
		return domain.SignedRemoval{}, err
	}
	if record.ClusterID != issuer.Cluster.ID || record.Status != "removed" || record.NodeID == "" || record.MembershipEpoch == 0 {
		return domain.SignedRemoval{}, errors.New("invalid removal record")
	}
	pair, err := tls.X509KeyPair([]byte(issuer.CA), []byte(issuer.Key))
	if err != nil {
		return domain.SignedRemoval{}, err
	}
	key, ok := pair.PrivateKey.(ed25519.PrivateKey)
	if !ok {
		return domain.SignedRemoval{}, errors.New("invalid issuer key")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return domain.SignedRemoval{}, err
	}
	return domain.SignedRemoval{Record: record, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, data))}, nil
}
func verifyRemoval(notice domain.SignedRemoval, trust domain.ClusterTrust, node domain.ID) error {
	r := notice.Record
	if r.ClusterID != trust.Cluster.ID || r.NodeID != node || r.MembershipEpoch != trust.Issuance.MembershipEpoch || r.Status != "removed" || r.RemovalVersion == 0 {
		return errors.New("removal does not match current cluster/node/epoch")
	}
	ca, err := parseCertificate(trust.Issuance.ClusterCA)
	if err != nil {
		return err
	}
	if shared.Fingerprint(ca.RawSubjectPublicKeyInfo) != trust.Issuance.ClusterFingerprint {
		return errors.New("removal trust pin mismatch")
	}
	key, ok := ca.PublicKey.(ed25519.PublicKey)
	if !ok {
		return errors.New("invalid removal issuer")
	}
	signature, err := base64.StdEncoding.DecodeString(notice.Signature)
	if err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, data, signature) {
		return errors.New("invalid removal signature")
	}
	return nil
}

// Retire commits signed removal and autojoin suppression before clearing trust.
// The caller must close the broker before its old storage is quarantined.
func (l *Local) VerifyRemoval(notice domain.SignedRemoval) error {
	if l.Trust == nil {
		return errors.New("node is not enrolled")
	}
	return verifyRemoval(notice, *l.Trust, l.Identity.NodeID)
}
func (l *Local) Retire(notice domain.SignedRemoval) error {
	if l.Trust == nil {
		return errors.New("node is not enrolled")
	}
	if err := verifyRemoval(notice, *l.Trust, l.Identity.NodeID); err != nil {
		return err
	}
	if l.Trust.Cluster.IssuerNodeID == l.Identity.NodeID {
		return errors.New("sole issuer cannot retire")
	}
	r := retirement{Notice: notice, Trust: *l.Trust}
	if err := l.save("trust/removal.json", r); err != nil {
		return err
	}
	return l.finishRetirement(r)
}
func (l *Local) finishRetirement(r retirement) error {
	if err := verifyRemoval(r.Notice, r.Trust, l.Identity.NodeID); err != nil {
		return err
	}
	root, err := os.OpenRoot(l.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	if !r.Complete {
		if err = root.MkdirAll("broker-retired", 0700); err != nil {
			return err
		}
		archive := fmt.Sprintf("broker-retired/%s-%d", shared.Filename(r.Trust.Cluster.ID), r.Notice.Record.MembershipEpoch)
		if err = root.Rename("broker", archive); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if l.Identity.ClusterID != nil {
		l.Identity.Version++
	}
	l.Identity.ClusterID, l.Identity.ClusterFingerprint, l.Identity.MembershipEpoch = nil, nil, nil
	if err = l.save("identity.json", l.Identity); err != nil {
		return err
	}
	if err = root.Remove("trust/membership.json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if l.Daemon, err = certificate(root, "daemon", false); err != nil {
		return err
	}
	if l.Broker, err = certificate(root, "broker", false); err != nil {
		return err
	}
	for _, path := range []string{"trust", "broker-retired", "."} {
		dir, err := root.Open(path)
		if err != nil {
			return err
		}
		err = errors.Join(dir.Sync(), dir.Close())
		if err != nil {
			return err
		}
	}
	l.Trust = nil
	r.Complete = true
	return l.save("trust/removal.json", r)
}

func (l *Local) checkRetiredEpoch(trust domain.ClusterTrust) error {
	var r retirement
	err := l.load("trust/removal.json", &r)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = verifyRemoval(r.Notice, r.Trust, l.Identity.NodeID); err != nil {
		return err
	}
	if trust.Cluster.ID == r.Notice.Record.ClusterID && trust.Issuance.MembershipEpoch <= r.Notice.Record.MembershipEpoch {
		return errors.New("membership epoch was retired")
	}
	return nil
}
func (l *Local) AutojoinSuppressed() bool {
	var r retirement
	return !errors.Is(l.load("trust/removal.json", &r), os.ErrNotExist)
}

// recoverRetirement runs before restoring any enrolled transport credentials.
func (l *Local) recoverRetirement(trust *domain.ClusterTrust) (bool, error) {
	var r retirement
	err := l.load("trust/removal.json", &r)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = verifyRemoval(r.Notice, r.Trust, l.Identity.NodeID); err != nil {
		return false, err
	}
	if l.Identity.ClusterID != nil && (*l.Identity.ClusterID != r.Trust.Cluster.ID || (l.Identity.MembershipEpoch != nil && *l.Identity.MembershipEpoch > r.Notice.Record.MembershipEpoch)) && trust == nil {
		return false, nil
	}
	if trust != nil && (trust.Cluster.ID != r.Trust.Cluster.ID || trust.Issuance.MembershipEpoch > r.Notice.Record.MembershipEpoch) {
		return false, nil
	}
	if !r.Complete || trust != nil || l.Identity.ClusterID != nil {
		return true, l.finishRetirement(r)
	}
	return true, nil
}
