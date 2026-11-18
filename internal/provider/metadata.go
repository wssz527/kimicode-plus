package provider

import (
	"net/http"
	"time"
)

// Register mounts provider accounts. Catalog discovery and the provider
// registry itself were only reachable from the separate model page; models are
// now configured on the official provider form, so nothing here serves them.
func (c *Client) Register(mux *http.ServeMux) {
	services := &Service{db: c.DB, now: func() time.Time { return time.Now().UTC() }}
	services.RegisterAccounts(mux, c.Secrets)
}
