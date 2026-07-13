package cmd

import (
	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/certpin"
	"github.com/MalteKiefer/ledgerline-cli/internal/config"
)

// newAPIClient builds an API client with trust-on-first-use certificate pinning
// enabled, storing pins in the config directory. All commands reach the server
// through this helper so pinning is applied uniformly.
func newAPIClient(server string, opts ...api.Option) (*api.Client, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	opts = append(opts, api.WithCertPinner(certpin.NewStore(dir)))
	return api.New(server, opts...)
}
