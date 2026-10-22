package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"nixmox/internal/domain"
	"nixmox/internal/secretio"
)

func (c *Client) ImportCredential(ctx context.Context, request domain.SecretImport, value io.Reader) (domain.Receipt, error) {
	return SendSecret(ctx, c.HTTP, c.Endpoint+"/api/v1/credentials/import", request, value)
}
func SendSecret(ctx context.Context, client *http.Client, endpoint string, request domain.SecretImport, value io.Reader) (domain.Receipt, error) {
	var receipt domain.Receipt
	body, contentType := secretio.Encode(request, value)
	defer body.Close()
	r, err := http.NewRequestWithContext(ctx, "POST", endpoint, body)
	if err != nil {
		return receipt, err
	}
	r.Header.Set("Content-Type", contentType)
	response, err := client.Do(r)
	if err != nil {
		return receipt, domain.Err("OUTCOME_UNKNOWN", "Secret import reply unavailable; retry with the same import ID.", 503)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return receipt, domain.Err("OUTCOME_UNKNOWN", "Invalid secret import acknowledgment.", 503)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		var envelope struct {
			Error *domain.Error `json:"error"`
		}
		if json.Unmarshal(data, &envelope) != nil || envelope.Error == nil {
			return receipt, domain.Err("OUTCOME_UNKNOWN", "Secret import acknowledgment unavailable.", 503)
		}
		envelope.Error.Status = response.StatusCode
		return receipt, envelope.Error
	}
	if domain.StrictJSON(data, &receipt) != nil || receipt.CommandID != request.ID {
		return receipt, domain.Err("OUTCOME_UNKNOWN", "Secret import acknowledgment differs from the original request.", 503)
	}
	return receipt, nil
}
