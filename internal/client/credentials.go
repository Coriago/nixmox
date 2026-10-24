package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
)

func (c *Client) Credentials(ctx context.Context, page ...domain.PageRequest) (domain.Page[domain.Credential], error) {
	return listPage[domain.Credential](ctx, c, "credentials", page...)
}
func (c *Client) Credential(ctx context.Context, id domain.ID) (domain.Credential, error) {
	return get[domain.Credential](ctx, c, "credentials/"+url.PathEscape(string(id)))
}

// ImportCredential never places a value in an ordinary command, URL or retry buffer.
func (c *Client) ImportCredential(ctx context.Context, in engine.SecretIngress, value io.Reader) (domain.Receipt, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/api/v1/credentials/import", io.LimitReader(value, domain.CommandPayloadLimit+1))
	if err != nil {
		return domain.Receipt{}, err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("Nx-Command", string(in.CommandID))
	request.Header.Set("Nx-Holder", string(in.HolderID))
	request.Header.Set("Nx-Name", in.Name)
	request.Header.Set("Nx-Kind", in.Kind)
	response, err := c.http.Do(request)
	if err != nil {
		return domain.Receipt{}, &UnknownSubmission{CommandID: in.CommandID}
	}
	var receipt domain.Receipt
	err = decodeResponse(response, &receipt)
	if err == nil && receipt.CommandID == in.CommandID && receipt.Admission == "accepted" && receipt.TaskID != nil {
		return receipt, nil
	}
	var failure *APIError
	if errors.As(err, &failure) && failure.StatusCode < 500 {
		return receipt, err
	}
	return receipt, &UnknownSubmission{CommandID: in.CommandID}
}
