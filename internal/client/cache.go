package client

import (
	"context"
	"net/url"
	"nixmox/internal/domain"
)

func (c *Client) CacheStatus(ctx context.Context) (domain.CacheStatus, error) {
	var status domain.CacheStatus
	err := c.request(ctx, "GET", "cache/status", nil, &status)
	return status, err
}
func (c *Client) CacheLocations(ctx context.Context, path string) ([]domain.CacheLocation, error) {
	var locations []domain.CacheLocation
	err := c.request(ctx, "GET", "cache/locations?"+url.Values{"path": {path}}.Encode(), nil, &locations)
	return locations, err
}
