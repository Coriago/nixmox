// Package identity owns local machine keys and enrollment trust, never HTTP handlers.
package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"nixmox/internal/domain"
	"nixmox/internal/engine/internal/shared"
)

// Local contains private TLS material; only identity/trust records are public.
// The caller must hold the node's journal lock throughout initialization/use.
type Local struct {
	directory string
	Trust     *domain.ClusterTrust
	Identity  domain.Identity
	Daemon    tls.Certificate
	Broker    tls.Certificate
}

func Open(ctx context.Context, directory string) (*Local, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	local := &Local{directory: directory}
	data, err := root.ReadFile("identity.json")
	fresh := errors.Is(err, os.ErrNotExist)
	if err != nil && !fresh {
		return nil, err
	}
	if !fresh {
		if err = json.Unmarshal(data, &local.Identity); err != nil {
			return nil, err
		}
		id := local.Identity
		if id.SchemaVersion != domain.SchemaVersion || id.Version == 0 || id.NodeID == "" || id.PublicKey == "" {
			return nil, errors.New("invalid persisted identity; refusing to replace it")
		}
	}
	if local.Daemon, err = certificate(root, "daemon", fresh); err != nil {
		return nil, err
	}
	if local.Broker, err = certificate(root, "broker", fresh); err != nil {
		return nil, err
	}
	public := base64.StdEncoding.EncodeToString(local.Daemon.Leaf.RawSubjectPublicKeyInfo)
	if fresh {
		local.Identity = domain.Identity{SchemaVersion: domain.SchemaVersion, Version: 1,
			NodeID: domain.ID("n_" + uuid.NewString()), KeyID: domain.ID("key_" + uuid.NewString()), PublicKey: public}
		data, err = json.Marshal(local.Identity)
		if err != nil {
			return nil, err
		}
		if err = writeFile(root, "identity.json", data); err != nil {
			return nil, err
		}
	} else if local.Identity.PublicKey != public {
		return nil, errors.New("identity key mismatch; refusing to replace trust")
	}
	var trust domain.ClusterTrust
	err = local.load("trust/membership.json", &trust)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var current *domain.ClusterTrust
	if err == nil {
		current = &trust
	}
	retired, recoveryErr := local.recoverRetirement(current)
	if recoveryErr != nil {
		return nil, recoveryErr
	}
	if !retired {
		if current != nil {
			if err = local.Install(trust); err != nil {
				return nil, err
			}
		} else if local.Identity.ClusterID != nil {
			return nil, fmt.Errorf("persisted cluster trust unavailable: %w", err)
		}
	}
	return local, nil
}

func certificate(root *os.Root, role string, allowCreate bool) (tls.Certificate, error) {
	dir := "pki/" + role
	keyPEM, err := root.ReadFile(dir + "/key.pem")
	if errors.Is(err, os.ErrNotExist) && allowCreate {
		_, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return tls.Certificate{}, e
		}
		der, e := x509.MarshalPKCS8PrivateKey(key)
		if e != nil {
			return tls.Certificate{}, e
		}
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		err = writeFile(root, dir+"/key.pem", keyPEM)
	}
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("%s key: %w", role, err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return tls.Certificate{}, errors.New("invalid private key PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return tls.Certificate{}, err
	}
	signer, ok := key.(ed25519.PrivateKey)
	if !ok {
		return tls.Certificate{}, errors.New("machine key must be Ed25519")
	}
	// Unjoined trust is local-only: self-signed endpoints trust one another's
	// exact certificates. This is not a cluster CA or an enrollment credential.
	now := time.Now()
	template := &x509.Certificate{Subject: pkix.Name{CommonName: "nixmox-local-" + role},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(domain.CertificateLifetime),
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, signer.Public(), signer)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err = writeFile(root, dir+"/cert.pem", certPEM); err != nil {
		return tls.Certificate{}, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err == nil {
		pair.Leaf, err = x509.ParseCertificate(der)
	}
	return pair, err
}

func (l *Local) ServerTLS() *tls.Config {
	pool := x509.NewCertPool()
	if l.Trust != nil {
		pool.AppendCertsFromPEM([]byte(l.Trust.Issuance.ClusterCA))
	} else {
		pool.AddCert(l.Daemon.Leaf)
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{l.Broker},
		ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert}
}

func (l *Local) ClientTLS() *tls.Config {
	pool := x509.NewCertPool()
	name := ""
	if l.Trust != nil {
		pool.AppendCertsFromPEM([]byte(l.Trust.Issuance.ClusterCA))
		name = shared.Token(l.Identity.NodeID)
	} else {
		pool.AddCert(l.Broker.Leaf)
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{l.Daemon}, RootCAs: pool, ServerName: name}
}

func (l *Local) DaemonKey() domain.Digest {
	return shared.Fingerprint(l.Daemon.Leaf.RawSubjectPublicKeyInfo)
}

func writeFile(root *os.Root, path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := root.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temp := path + ".new"
	defer root.Remove(temp)
	file, err := root.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	if err = root.Rename(temp, path); err != nil {
		return err
	}
	for {
		parent, err := root.Open(dir)
		if err != nil {
			return err
		}
		err = errors.Join(parent.Sync(), parent.Close())
		if err != nil || dir == "." {
			return err
		}
		dir = filepath.Dir(dir)
	}
}
