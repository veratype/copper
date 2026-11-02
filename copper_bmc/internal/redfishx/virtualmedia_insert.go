package redfishx

import (
	"fmt"
	"strings"

	"github.com/stmcginnis/gofish/schemas"
)

// insertAttempt records one InsertMedia (or PATCH) try for admin visibility.
type insertAttempt struct {
	Device string `json:"device"`
	Source string `json:"source"`
	Method string `json:"method"`
	Params string `json:"params"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
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
	c.logf("info", "virtual-media insert begin device=%s source=%s supports_action=%v image=%s media_types=%v inserted=%v current_image=%q connected=%s",
		device, source, vm.SupportsMediaInsert, imageURL, vmMediaTypes(vm),
		derefBool(vm.Inserted), vm.Image, vm.ConnectedVia)

	// SuperMicro (and Redfish generally) reject Insert while a slot still has media.
	// Clear first; never hard-fail the admin if eject is flaky — keep trying insert.
	c.ensureVirtualMediaEmpty(vm)

	// Many BMCs (esp. SuperMicro) return bare HTTP 412 when If-Match/ETag is sent.
	vm.DisableEtagMatch(true)
	c.logf("info", "virtual-media If-Match disabled on %s for insert/patch (BMC ETag quirks)", device)

	strategies := c.buildInsertStrategies(vm, imageURL)
	c.logf("info", "virtual-media will try %d insert strategies on %s", len(strategies), device)

	var errs []string
	saw412 := false
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
		msg := insertErr.Error()
		att.Error = msg
		attempts = append(attempts, att)
		errs = append(errs, fmt.Sprintf("%s: %s", s.name, shortRedfishErr(msg)))
		if looksPrecondition(msg) {
			saw412 = true
			c.logf("warn", "virtual-media attempt soft-fail method=%s device=%s (BMC precondition — often busy slot or ETag): %s",
				s.name, device, shortRedfishErr(msg))
		} else {
			c.logf("warn", "virtual-media attempt failed method=%s device=%s: %s",
				s.name, device, shortRedfishErr(msg))
		}

		// Mid-ladder: if BMC still looks busy, try another clear then continue.
		if looksBusySlot(msg) || (looksPrecondition(msg) && i == 0) {
			c.logf("info", "virtual-media re-clearing %s after reject, then continuing strategies", device)
			c.ensureVirtualMediaEmpty(vm)
			vm.DisableEtagMatch(true)
		}
	}

	// Last resort: PATCH without ETag — try Image-only first (SuperMicro-friendly), then full.
	for _, patch := range []struct {
		name string
		body map[string]any
	}{
		{"patch-image-only", map[string]any{"Image": imageURL}},
		{"patch-resource", map[string]any{
			"Image": imageURL, "Inserted": true, "WriteProtected": true,
		}},
	} {
		c.logf("info", "virtual-media attempt fallback method=%s device=%s", patch.name, device)
		patchAtt := insertAttempt{
			Device: device,
			Source: source,
			Method: patch.name,
			Params: describePatchBody(patch.body),
		}
		patchErr := vm.Patch(vm.ODataID, patch.body)
		if patchErr == nil {
			patchAtt.OK = true
			attempts = append(attempts, patchAtt)
			c.logf("info", "virtual-media SUCCESS method=%s device=%s", patch.name, device)
			return patch.name, attempts, nil
		}
		msg := patchErr.Error()
		if looksPrecondition(msg) {
			saw412 = true
		}
		patchAtt.Error = msg
		attempts = append(attempts, patchAtt)
		errs = append(errs, fmt.Sprintf("%s: %s", patch.name, shortRedfishErr(msg)))
		c.logf("warn", "virtual-media fallback failed method=%s device=%s: %s",
			patch.name, device, shortRedfishErr(msg))
	}

	c.logf("error", "virtual-media all insert strategies failed on %s (attempts=%d saw_412=%v)",
		device, len(attempts), saw412)
	return "", attempts, fmt.Errorf("%s", politeInsertFailure(device, saw412, errs))
}

// ensureVirtualMediaEmpty ejects when Inserted or Image is set. Errors are logged
// and swallowed — insert strategies still run so one flaky eject does not block the admin.
func (c *Client) ensureVirtualMediaEmpty(vm *schemas.VirtualMedia) {
	if vm == nil {
		return
	}
	device := vmLabel(vm)
	inserted := derefBool(vm.Inserted)
	image := strings.TrimSpace(vm.Image)
	if !inserted && image == "" {
		c.logf("info", "virtual-media slot %s already empty — ready for insert", device)
		return
	}

	c.logf("info", "virtual-media clearing busy slot %s before insert (inserted=%v image=%q connected=%s)",
		device, inserted, image, vm.ConnectedVia)
	vm.DisableEtagMatch(true)

	if _, ejectErr := vm.EjectMedia(); ejectErr == nil {
		c.logf("info", "virtual-media cleared %s via eject-action", device)
		// Best-effort: forget stale Image in our copy so later logic sees "empty".
		vm.Image = ""
		f := false
		vm.Inserted = &f
		return
	} else {
		c.logf("warn", "virtual-media eject-action on %s did not succeed (%s) — trying PATCH clear",
			device, shortRedfishErr(ejectErr.Error()))
	}

	cleared := false
	for _, body := range []map[string]any{
		{"Inserted": false, "Image": nil},
		{"Inserted": false},
		{"Image": nil},
	} {
		if err := vm.Patch(vm.ODataID, body); err != nil {
			c.logf("warn", "virtual-media PATCH clear on %s body=%s: %s",
				device, describePatchBody(body), shortRedfishErr(err.Error()))
			continue
		}
		c.logf("info", "virtual-media cleared %s via PATCH (%s)", device, describePatchBody(body))
		cleared = true
		vm.Image = ""
		f := false
		vm.Inserted = &f
		break
	}
	if !cleared {
		c.logf("warn", "virtual-media could not fully clear %s — continuing with insert anyway (BMC may still accept)", device)
	}
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

	// 1) Prefer ActionInfo when the BMC advertises allowed parameters.
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
		if allowed["MediaType"] {
			params.MediaType = preferredMediaType(vm)
		}
		out = append(out, insertStrategy{name: "actioninfo", params: params})
	} else if err != nil {
		c.logf("info", "virtual-media ActionInfo unavailable on %s: %v — using fixed strategy ladder", vmLabel(vm), err)
	} else {
		c.logf("info", "virtual-media ActionInfo empty on %s — using fixed strategy ladder", vmLabel(vm))
	}

	// 2) Image-only early — SuperMicro X11/X12 often reject Inserted/WriteProtected.
	out = append(out,
		insertStrategy{
			name:   "image-only",
			params: &schemas.VirtualMediaInsertMediaParameters{Image: imageURL},
		},
		insertStrategy{
			name: "image-and-inserted",
			params: &schemas.VirtualMediaInsertMediaParameters{
				Image:    imageURL,
				Inserted: &inserted,
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
			name: "standard-with-protocol",
			params: &schemas.VirtualMediaInsertMediaParameters{
				Image:                imageURL,
				Inserted:             &inserted,
				WriteProtected:       &writeProtected,
				TransferProtocolType: &proto,
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
	)

	// 3) Non-standard MediaType last.
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

func describePatchBody(body map[string]any) string {
	var parts []string
	for k, v := range body {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
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

func looksPrecondition(msg string) bool {
	low := strings.ToLower(msg)
	return strings.Contains(low, "412") ||
		strings.Contains(low, "precondition") ||
		strings.Contains(low, "etag")
}

func looksBusySlot(msg string) bool {
	low := strings.ToLower(msg)
	return strings.Contains(low, "already") ||
		strings.Contains(low, "inserted") && strings.Contains(low, "true") ||
		strings.Contains(low, "not null") ||
		strings.Contains(low, "image") && strings.Contains(low, "reject")
}

// shortRedfishErr trims multi-line BMC dumps to one short line for logs/UI.
func shortRedfishErr(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "(no detail from BMC)"
	}
	if i := strings.IndexByte(msg, '\n'); i > 0 {
		msg = strings.TrimSpace(msg[:i])
	}
	if len(msg) > 160 {
		return msg[:157] + "..."
	}
	return msg
}

func politeInsertFailure(device string, saw412 bool, errs []string) string {
	hint := "Eject virtual media on the BMC (or use Eject in Copper), confirm the ISO URL is reachable from the BMC on the OOB network, then try Mount again."
	if saw412 {
		hint = "This BMC returned precondition errors (common on SuperMicro when a slot is still busy or ETag matching fails). " + hint
	}
	detail := strings.Join(errs, "; ")
	if len(detail) > 300 {
		detail = detail[:297] + "..."
	}
	return fmt.Sprintf(
		"Could not attach media on %s. %s Last BMC replies: %s",
		device, hint, detail,
	)
}

// politeMountFailure summarizes multi-slot failure for the admin UI / job log.
func politeMountFailure(slotCount, attemptCount int, last error) string {
	base := fmt.Sprintf(
		"Could not mount virtual media after trying %d slot(s) (%d insert attempt(s)). "+
			"Please eject any existing virtual media on the BMC, verify the image URL is on the OOB/BMC network, then retry Mount.",
		slotCount, attemptCount,
	)
	if last == nil {
		return base
	}
	msg := shortRedfishErr(last.Error())
	if msg == "" {
		return base
	}
	return base + " Detail: " + msg
}
