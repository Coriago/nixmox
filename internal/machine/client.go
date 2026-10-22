package machine

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/pki"
)

// Bootstrap TLS proves possession, then pins the first server key. This trust
// policy is confined to enrollment; normal machine and broker clients verify CA.
func bootstrapTLS(pair tls.Certificate, pin string) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &pair, nil },
		VerifyConnection: func(s tls.ConnectionState) error {
			if len(s.PeerCertificates) == 0 {
				return errors.New("missing enrollment certificate")
			}
			c := s.PeerCertificates[0]
			if time.Now().Before(c.NotBefore) || time.Now().After(c.NotAfter) || pki.Fingerprint(c.PublicKey) == "" {
				return domain.Err("INVALID_CERTIFICATE", "Enrollment certificate is invalid or expired.", 409)
			}
			if pin != "" && pki.Fingerprint(c.PublicKey) != pin {
				return domain.Err("IDENTITY_CONFLICT", "Enrollment server key changed during this attempt.", 409)
			}
			return nil
		}}
}

// Probe sends no enrollment request. The caller must persist its result before
// constructing a client or sending any request that can create cluster state.
func Probe(ctx context.Context, address string, pair tls.Certificate) (*x509.Certificate, error) {
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: bootstrapTLS(pair, "")}
	c, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.(*tls.Conn).ConnectionState().PeerCertificates[0], nil
}

type Client struct {
	http    *http.Client
	address string
}

func EnrollmentClient(address string, pair tls.Certificate, pin string) (*Client, error) {
	if len(pin) != 64 {
		return nil, errors.New("enrollment server pin required")
	}
	tr := &http.Transport{TLSClientConfig: bootstrapTLS(pair, pin), TLSHandshakeTimeout: 5 * time.Second,
		DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, MaxConnsPerHost: 1,
		MaxIdleConns: 1, IdleConnTimeout: 10 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 8 << 10}
	return &Client{http: &http.Client{Transport: tr, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, address: address}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) request(ctx context.Context, method, path string, payload, out any) error {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, "https://"+c.address+"/api/v1/enrollment/requests"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return domain.Err("INVALID_RESPONSE", "Enrollment response exceeds its limit.", 502)
	}
	if resp.StatusCode != http.StatusOK {
		var envelope struct {
			Error *domain.Error `json:"error"`
		}
		if domain.StrictJSON(data, &envelope) != nil || envelope.Error == nil {
			return domain.Err("UNAVAILABLE", "Enrollment endpoint unavailable.", 503)
		}
		envelope.Error.Status = resp.StatusCode
		return envelope.Error
	}
	if domain.StrictJSON(data, out) != nil {
		return domain.Err("INVALID_RESPONSE", "Invalid enrollment response.", 502)
	}
	return nil
}
func (c *Client) Submit(ctx context.Context, r domain.EnrollmentRequest) (domain.JoinRequest, error) {
	var v domain.JoinRequest
	err := c.request(ctx, "POST", "", r, &v)
	return v, err
}
func (c *Client) Read(ctx context.Context, id string) (domain.JoinRequest, error) {
	var v domain.JoinRequest
	if !domain.SafeID.MatchString(id) {
		return v, errors.New("invalid request ID")
	}
	err := c.request(ctx, "GET", "/"+id, nil, &v)
	return v, err
}
func (c *Client) Certificates(ctx context.Context, id string) (domain.Issuance, error) {
	var v domain.Issuance
	if !domain.SafeID.MatchString(id) {
		return v, errors.New("invalid request ID")
	}
	err := c.request(ctx, "POST", "/"+id+"/certificates", struct{}{}, &v)
	return v, err
}
