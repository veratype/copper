package redfishx

import (
	"fmt"
	"strings"

	"github.com/stmcginnis/gofish/schemas"
)

// insertAttempt records one InsertMedia (or PATCH) try for admin visibility.
type insertAttempt struct {
	Device  string `json:"device"`
	Source  string `json:"source"`
	Method  string `json:"method"`
	Params  string `json:"params"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

type insertStrategy struct {
	name   string
	params *schemas.VirtualMediaInsertMediaParameters
}

func (c *Client) insertMediaRobust(
	vm *schemas.VirtualMedia,
	source string,
	imageURL string,
) (method string, attempts []insertAttempt, err error) {
	device := vmLabel(vm)
	c.logf("info", "virtual-media insert begin device=%s source=%s supports_action=%v image=%s media_types=%v",
		device, source, vm.SupportsMediaInsert, imageURL, vmMediaTypes(vm))

	if derefBool(vm.Inserted) {
		c.logf("info", "virtual-media eject existing media on %s (image was %q)", device, vm.Image)
		if _, ejectErr := vm.EjectMedia(); ejectErr != nil {
			c.logf("warn", "virtual-media eject on %s failed: %v — continuing with insert attempts", device, ejectErr)
		} else {
			c.logf("info", "virtual-media eject on %s ok", device)
		}
	}

	strategies := c.buildInsertStrategies(vm, imageURL)
	c.logf("info", "virtual-media will try %d insert strategies on %s", len(strategies), device)

	var errs []string
	for i, s := range strategies {
		c.logf("info", "virtual-media attempt %d/%d method=%s params={%s} device=%s",
			i+1, len(strategies), s.name, describeInsertParams(s.params), device)
		att := insertAttempt{
			Device: device,
			Source: source,
			Method: s.name,
			Params: describeInsertParams(s.params),
		}
		_, insertErr := vm.InsertMedia(s.params)
		if insertErr == nil {
			att.OK = true
			attempts = append(attempts, att)
			c.logf("info", "virtual-media SUCCESS method=%s device=%s", s.name, device)
			return s.name, attempts, nil
		}
		att.Error = insertErr.Error()
		attempts = append(attempts, att)
		errs = append(errs, fmt.Sprintf("%s: %v", s.name, insertErr))
		c.logf("warn", "virtual-media attempt failed method=%s device=%s: %v", s.name, device, insertErr)
	}

	// Last resort: explicit PATCH of the VirtualMedia resource (some BMCs omit the action).
	c.logf("info", "virtual-media attempt fallback method=patch-resource device=%s", device)
	patchAtt := insertAttempt{
		Device: device,
		Source: source,
		Method: "patch-resource",
		Params: "Image,Inserted=true,WriteProtected=true",
	}
	inserted := true
	writeProtected := true
	patchErr := vm.Patch(vm.ODataID, map[string]any{
		"Image":          imageURL,
		"Inserted":       inserted,
		"WriteProtected": writeProtected,
	})
	if patchErr == nil {
		patchAtt.OK = true
		attempts = append(attempts, patchAtt)
		c.logf("info", "virtual-media SUCCESS method=patch-resource device=%s", device)
		return "patch-resource", attempts, nil
	}
	patchAtt.Error = patchErr.Error()
	attempts = append(attempts, patchAtt)
	errs = append(errs, fmt.Sprintf("patch-resource: %v", patchErr))
	c.logf("error", "virtual-media all insert strategies failed on %s", device)

	return "", attempts, fmt.Errorf("insert media on %s failed after %d attempts: %s",
		device, len(attempts), strings.Join(errs, " | "))
}

func (c *Client) buildInsertStrategies(vm *schemas.VirtualMedia, imageURL string) []insertStrategy {
	inserted := true
	writeProtected := true
	proto := schemas.HTTPTransferProtocolType
	if strings.HasPrefix(strings.ToLower(imageURL), "https://") {
		proto = schemas.HTTPSTransferProtocolType
	}
	stream := schemas.StreamTransferMethod

	var out []insertStrategy

	// 1) Prefer ActionInfo when the BMC advertises allowed parameters (most universal).
	if info, err := vm.InsertMediaActionInfo(); err == nil && info != nil && len(info.Parameters) > 0 {
		allowed := map[string]bool{}
		var names []string
		for _, p := range info.Parameters {
			allowed[p.Name] = true
			names = append(names, p.Name)
		}
		c.logf("info", "virtual-media ActionInfo parameters on %s: %s", vmLabel(vm), strings.Join(names, ", "))
		params := &schemas.VirtualMediaInsertMediaParameters{Image: imageURL}
		if allowed["Inserted"] {
			params.Inserted = &inserted
		}
		if allowed["WriteProtected"] {
			params.WriteProtected = &writeProtected
		}
		if allowed["TransferProtocolType"] {
			params.TransferProtocolType = &proto
		}
		if allowed["TransferMethod"] {
			params.TransferMethod = &stream
		}
		// Only include MediaType if THIS BMC lists it (rare / non-standard).
		if allowed["MediaType"] {
			params.MediaType = preferredMediaType(vm)
		}
		out = append(out, insertStrategy{name: "actioninfo", params: params})
	} else if err != nil {
		c.logf("info", "virtual-media ActionInfo unavailable on %s: %v — using fixed strategy ladder", vmLabel(vm), err)
	} else {
		c.logf("info", "virtual-media ActionInfo empty on %s — using fixed strategy ladder", vmLabel(vm))
	}

	// 2) DMTF-shaped payloads without MediaType (strict BMCs e.g. HPE iLO).
	out = append(out,
		insertStrategy{
			name: "standard-with-protocol",
			params: &schemas.VirtualMediaInsertMediaParameters{
				Image:                imageURL,
				Inserted:             &inserted,
				WriteProtected:       &writeProtected,
				TransferProtocolType: &proto,
			},
		},
		insertStrategy{
			name: "standard-no-protocol",
			params: &schemas.VirtualMediaInsertMediaParameters{
				Image:          imageURL,
				Inserted:       &inserted,
				WriteProtected: &writeProtected,
			},
		},
		insertStrategy{
			name: "stream-with-protocol",
			params: &schemas.VirtualMediaInsertMediaParameters{
				Image:                imageURL,
				Inserted:             &inserted,
				WriteProtected:       &writeProtected,
				TransferProtocolType: &proto,
				TransferMethod:       &stream,
			},
		},
		insertStrategy{
			name: "image-and-inserted",
			params: &schemas.VirtualMediaInsertMediaParameters{
				Image:    imageURL,
				Inserted: &inserted,
			},
		},
		insertStrategy{
			name: "image-only",
			params: &schemas.VirtualMediaInsertMediaParameters{
				Image: imageURL,
			},
		},
	)

	// 3) Non-standard MediaType last — only some vendors want it; others reject it.
	mt := preferredMediaType(vm)
	out = append(out, insertStrategy{
		name: "vendor-mediatype-" + string(mt),
		params: &schemas.VirtualMediaInsertMediaParameters{
			Image:                imageURL,
			Inserted:             &inserted,
			WriteProtected:       &writeProtected,
			TransferProtocolType: &proto,
			MediaType:            mt,
		},
	})

	return dedupeStrategies(out)
}

func preferredMediaType(vm *schemas.VirtualMedia) schemas.VirtualMediaType {
	for _, t := range vm.MediaTypes {
		if t == schemas.CDVirtualMediaType || t == schemas.DVDVirtualMediaType {
			return t
		}
	}
	return schemas.CDVirtualMediaType
}

func dedupeStrategies(in []insertStrategy) []insertStrategy {
	seen := map[string]bool{}
	var out []insertStrategy
	for _, s := range in {
		key := s.name + "|" + describeInsertParams(s.params)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}

func describeInsertParams(p *schemas.VirtualMediaInsertMediaParameters) string {
	if p == nil {
		return ""
	}
	var parts []string
	if p.Image != "" {
		parts = append(parts, "Image")
	}
	if p.Inserted != nil {
		parts = append(parts, fmt.Sprintf("Inserted=%v", *p.Inserted))
	}
	if p.WriteProtected != nil {
		parts = append(parts, fmt.Sprintf("WriteProtected=%v", *p.WriteProtected))
	}
	if p.TransferProtocolType != nil {
		parts = append(parts, fmt.Sprintf("TransferProtocolType=%s", *p.TransferProtocolType))
	}
	if p.TransferMethod != nil {
		parts = append(parts, fmt.Sprintf("TransferMethod=%s", *p.TransferMethod))
	}
	if p.MediaType != "" {
		parts = append(parts, fmt.Sprintf("MediaType=%s", p.MediaType))
	}
	return strings.Join(parts, ", ")
}

func vmLabel(vm *schemas.VirtualMedia) string {
	if vm == nil {
		return "?"
	}
	if vm.ID != "" {
		return vm.ID
	}
	return vm.Name
}

func vmMediaTypes(vm *schemas.VirtualMedia) []string {
	var out []string
	for _, t := range vm.MediaTypes {
		out = append(out, string(t))
	}
	return out
}
