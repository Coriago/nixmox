package client

import (
	"context"
	"net/url"

	"nixmox/internal/domain"
)

func (c *Client) Repositories(ctx context.Context, page ...domain.PageRequest) (domain.Page[domain.Repository], error) {
	return listPage[domain.Repository](ctx, c, "repositories", page...)
}
func (c *Client) Repository(ctx context.Context, id domain.ID) (domain.Repository, error) {
	return get[domain.Repository](ctx, c, "repositories/"+url.PathEscape(string(id)))
}
func (c *Client) References(ctx context.Context, id domain.ID, page ...domain.PageRequest) (domain.Page[domain.Reference], error) {
	return listPage[domain.Reference](ctx, c, "repositories/"+url.PathEscape(string(id))+"/refs", page...)
}
func (c *Client) Configurations(ctx context.Context, page ...domain.PageRequest) (domain.Page[domain.Configuration], error) {
	return listPage[domain.Configuration](ctx, c, "configurations", page...)
}
func (c *Client) Configuration(ctx context.Context, id domain.ID) (domain.Configuration, error) {
	return get[domain.Configuration](ctx, c, "configurations/"+url.PathEscape(string(id)))
}
