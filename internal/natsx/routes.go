package natsx

import "net/url"

// ReloadRoutes disconnects removed routes without interrupting the surviving
// Raft group. The supervisor serializes calls with its membership transition.
func (e *EmbeddedServer) ReloadRoutes(routes []string) error {
	opts := e.options
	opts.Routes = nil
	for _, raw := range routes {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		opts.Routes = append(opts.Routes, u)
	}
	if err := e.Srv.ReloadOptions(&opts); err != nil {
		return err
	}
	e.options = opts
	return nil
}
