// Package netbox is the optional NetBox integration. NetBox writeback is an
// integration, not an emitted signal — it lives behind netbox.enabled in
// config and is disabled by default.
//
// Implementation lands per the v1 plan; this stub keeps the public surface.
package netbox

import (
	"context"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// Client reconciles discovered devices into a NetBox instance via the REST API.
type Client struct{}

// Reconcile syncs the supplied devices into NetBox. Stub.
func (c *Client) Reconcile(_ context.Context, _ []discovery.Device) error {
	return nil
}
