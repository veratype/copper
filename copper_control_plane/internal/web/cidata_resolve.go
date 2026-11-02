package web

import (
	"context"
	"fmt"
	"strings"

	"copper_control_plane/internal/provision"
	"copper_control_plane/internal/storage"
	"copper_control_plane/internal/store"
)

const (
	nicSelectByName  = "by_name"
	nicSelectDataNIC = "data_nic"
)

func normalizeNICSelect(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", nicSelectByName:
		return nicSelectByName
	case nicSelectDataNIC, "data", "lease":
		return nicSelectDataNIC
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

// preferProvisionedNetwork applies inventory Provisioned* fields over the form
// template (hostname / static address / gw / dns). Blank inventory fields leave
// the form/pattern values. index is 1-based.
func preferProvisionedNetwork(c provision.Cidata, srv *store.Server, index int, batch bool) (provision.Cidata, error) {
	c = c.Normalize()
	if srv == nil {
		return c, fmt.Errorf("nil server")
	}

	if pinned := strings.TrimSpace(srv.ProvisionedHostname); pinned != "" {
		c.Hostname = pinned
	} else {
		hostPat := c.Hostname
		if hostPat == "" {
			if batch {
				hostPat = "{name}"
			} else {
				hostPat = srv.Name
			}
		}
		hostname, err := provision.ExpandPattern(hostPat, srv.Name, index)
		if err != nil {
			return c, fmt.Errorf("hostname pattern: %w", err)
		}
		c.Hostname = hostname
	}

	if pinned := strings.TrimSpace(srv.ProvisionedIPCIDR); pinned != "" {
		c.NetworkMode = provision.NetStatic
		c.AddressCIDR = pinned
	} else if c.NetworkMode == provision.NetStatic {
		addrPat := strings.TrimSpace(c.AddressCIDR)
		if addrPat == "" {
			return c, fmt.Errorf("static address (CIDR) is required")
		}
		if batch && !provision.HasIndexPattern(addrPat) {
			return c, fmt.Errorf("batch static IP needs an index pattern (e.g. 10.0.0.{n+10}/24); got %q", addrPat)
		}
		addr, err := provision.ExpandAddressCIDR(addrPat, index)
		if err != nil {
			return c, fmt.Errorf("address pattern: %w", err)
		}
		c.AddressCIDR = addr
	}

	if c.NetworkMode == provision.NetStatic {
		if pinned := strings.TrimSpace(srv.ProvisionedGW); pinned != "" {
			c.Gateway = pinned
		}
		if pinned := strings.TrimSpace(srv.ProvisionedDNS); pinned != "" {
			c.DNS = splitProvisionedDNS(pinned)
		}
	}
	return c, nil
}

// expandHostCidata builds per-host cidata from a form template.
// index is 1-based position in the provision batch.
// Non-empty store.Server Provisioned* fields win over the form template.
func (s *Server) expandHostCidata(ctx context.Context, p provisionPending, tmpl provision.Cidata, nicSelect string, index int, batch bool) (provision.Cidata, error) {
	c, err := preferProvisionedNetwork(tmpl, p.srv, index, batch)
	if err != nil {
		return c, err
	}
	nicSelect = normalizeNICSelect(nicSelect)

	switch nicSelect {
	case nicSelectByName:
		if err := s.resolveNICByName(ctx, p, &c); err != nil {
			return c, err
		}
	case nicSelectDataNIC:
		if err := s.resolveNICByDataLease(ctx, p, &c); err != nil {
			return c, err
		}
	default:
		return c, fmt.Errorf("nic_select must be %q or %q", nicSelectByName, nicSelectDataNIC)
	}

	c = c.Normalize()
	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

func (s *Server) resolveNICByName(ctx context.Context, p provisionPending, c *provision.Cidata) error {
	want := strings.TrimSpace(c.Interface)
	if want == "" {
		return fmt.Errorf("interface name is required when nic_select=by_name")
	}
	formMAC := strings.TrimSpace(c.MAC)
	nics, err := storage.ListNICs(ctx, p.host)
	if err != nil {
		if formMAC != "" {
			// Single-host fallback when live NIC probe fails but UI already had a MAC.
			return nil
		}
		return fmt.Errorf("list NICs: %w", err)
	}
	for _, n := range nics {
		if n.Name == want {
			c.Interface = n.Name
			if mac := strings.TrimSpace(n.MAC); mac != "" {
				c.MAC = mac
			} else if formMAC != "" {
				c.MAC = formMAC
			}
			if strings.TrimSpace(c.MAC) == "" {
				return fmt.Errorf("interface %s has no MAC", want)
			}
			return nil
		}
	}
	names := make([]string, 0, len(nics))
	for _, n := range nics {
		names = append(names, n.Name)
	}
	return fmt.Errorf("interface %q not found (have: %s)", want, strings.Join(names, ", "))
}

func (s *Server) resolveNICByDataLease(ctx context.Context, p provisionPending, c *provision.Cidata) error {
	_, mac, err := s.dataIPForServer(p.srv)
	if err != nil {
		return fmt.Errorf("data NIC: %w", err)
	}
	mac = strings.ToLower(strings.TrimSpace(mac))
	c.MAC = mac

	nics, err := storage.ListNICs(ctx, p.host)
	if err != nil {
		// MAC alone is enough for netplan match.
		if c.Interface == "" {
			c.Interface = "id0"
		}
		return nil
	}
	for _, n := range nics {
		nm := strings.ToLower(strings.TrimSpace(n.MAC))
		if nm != "" && nm == mac {
			c.Interface = n.Name
			c.MAC = n.MAC
			return nil
		}
	}
	if c.Interface == "" {
		c.Interface = "id0"
	}
	return nil
}

func (s *Server) expandAllHostCidata(ctx context.Context, pend []provisionPending, tmpl provision.Cidata, nicSelect string) ([]provision.Cidata, error) {
	batch := len(pend) > 1
	out := make([]provision.Cidata, len(pend))
	for i, p := range pend {
		var c provision.Cidata
		err := s.withServerLock(ctx, p.srv.ID, func(locked context.Context) error {
			var eerr error
			c, eerr = s.expandHostCidata(locked, p, tmpl, nicSelect, i+1, batch)
			return eerr
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.srv.Name, err)
		}
		out[i] = c
	}
	return out, nil
}
