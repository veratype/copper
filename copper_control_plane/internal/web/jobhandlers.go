package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"copper_control_plane/internal/jobs"
	"copper_control_plane/internal/provision"
	"copper_control_plane/internal/softraid"
	"copper_control_plane/internal/storage"
	"copper_control_plane/internal/store"
)

func (s *Server) registerJobHandlers() {
	if s.jobs == nil {
		return
	}
	s.jobs.Register(jobs.TypeProvisionWrite, s.handleJobProvisionWrite)
	s.jobs.Register(jobs.TypeProvisionPreflight, s.handleJobProvisionPreflight)
	s.jobs.Register(jobs.TypeStorageDetect, s.handleJobStorageDetect)
	s.jobs.Register(jobs.TypeStorageConfig, s.handleJobStorageConfig)
	s.jobs.Register(jobs.TypeStorageExec, s.handleJobStorageExec)
	s.jobs.Register(jobs.TypeStorageWipePartitions, s.handleJobStorageWipePartitions)
	s.jobs.Register(jobs.TypeSoftRAIDScan, s.handleJobSoftRAIDScan)
	s.jobs.Register(jobs.TypeSoftRAIDExec, s.handleJobSoftRAIDExec)
	s.jobs.Register(jobs.TypeRedfishProbe, s.handleJobRedfishProbe)
	s.jobs.Register(jobs.TypeRedfishInventory, s.handleJobRedfishInventory)
	s.jobs.Register(jobs.TypeRedfishReset, s.handleJobRedfishReset)
	s.jobs.Register(jobs.TypeRedfishMount, s.handleJobRedfishMount)
	s.jobs.Register(jobs.TypeRedfishConvertUEFI, s.handleJobRedfishConvertUEFI)
	s.jobs.Register(jobs.TypeInventoryClaimNICs, s.handleJobInventoryClaimNICs)
}

type provisionJobPayload struct {
	ImageURL      string           `json:"image_url"`
	ImageSHA256   string           `json:"image_sha256,omitempty"`
	ImageSize     int64            `json:"image_size,omitempty"`
	ReuseImage    bool             `json:"reuse_image,omitempty"` // retry only: skip dd if checksum matches
	TargetDisk    string           `json:"target_disk"`
	Cidata        provision.Cidata `json:"cidata"`
	SkipPreflight bool             `json:"skip_preflight"`
	SetBootHDD    bool             `json:"set_boot_hdd"`
	Reboot        bool             `json:"reboot"`
}

type storageExecPayload struct {
	Args []string `json:"args"`
}

type softRAIDExecPayload struct {
	Action softraid.Action `json:"action"`
}

type mountJobPayload struct {
	ImageURL string `json:"image_url"`
	BootOnce bool   `json:"boot_once"`
	Reboot   bool   `json:"reboot"`
}

type claimNICsJobPayload struct {
	IP  string `json:"ip"`
	MAC string `json:"mac,omitempty"`
}

type resetJobPayload struct {
	ResetType string `json:"reset_type,omitempty"` // ForceRestart, On, ForceOff, …
}

type convertUEFIJobPayload struct {
	Reboot *bool `json:"reboot,omitempty"` // default true
}

func (s *Server) handleJobProvisionWrite(ctx context.Context, job *jobs.Job, progress jobs.Progress) error {
	var p provisionJobPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("bad payload: %w", err)
	}
	srv, err := s.store.GetServer(job.ServerID)
	if err != nil {
		return err
	}
	host, err := s.liveHost(srv)
	if err != nil {
		return err
	}
	req := provision.Request{
		ImageURL:      p.ImageURL,
		ImageSHA256:   p.ImageSHA256,
		ImageSize:     p.ImageSize,
		ReuseImage:    p.ReuseImage,
		TargetDisk:    p.TargetDisk,
		Cidata:        p.Cidata,
		SkipPreflight: p.SkipPreflight,
	}
	if err := provision.Run(ctx, host, req, func(phase, line string) {
		progress(phase, line)
	}); err != nil {
		return err
	}

	// Tag as soon as the image write succeeds — before eject/reboot, so a later
	// Redfish hiccup still leaves every finished host with "installed" (batch).
	progress("labels", `assign "`+store.LabelInstalled+`"`)
	if err := s.store.AddServerLabels(job.ServerID, []string{store.LabelInstalled}); err != nil {
		progress("labels", "warn: could not assign "+store.LabelInstalled+": "+err.Error())
	} else {
		progress("labels", `label "`+store.LabelInstalled+`" ok`)
	}

	if p.SetBootHDD {
		// efibootmgr already pinned BootOrder to this disk's ESP. A Redfish
		// Hdd Continuous override fights that on multi-disk HPE boxes (firmware
		// picks "some" HDD — often an old Proxmox volume). Clear the override
		// so NVRAM BootOrder wins.
		progress("boot", "clear BootSourceOverride (Disabled) so efibootmgr BootOrder wins")
		_, err := s.agent.SetBoot(srv, map[string]any{
			"enabled": "Disabled",
		})
		if err != nil {
			progress("boot", "Disabled-only rejected — retrying with target=Hdd: "+err.Error())
			_, err = s.agent.SetBoot(srv, map[string]any{
				"target": "Hdd", "enabled": "Disabled",
			})
		}
		if err != nil {
			return fmt.Errorf("post-boot clear BootSourceOverride: %w", err)
		}
		progress("boot", fmt.Sprintf("BootSourceOverride Disabled ok (disk %s; BootOrder from efibootmgr)", p.TargetDisk))
	}

	// Always eject live ISO / iLO Virtual CD after a successful write so the
	// next boot cannot fall back into the provisioning image (BootCurrent:000D).
	progress("media", "eject virtual media (all inserted slots)")
	ejectRaw, ejectErr := s.agent.EjectVirtualMedia(srv)
	if ejectErr != nil {
		return fmt.Errorf("post-provision eject virtual media: %w", ejectErr)
	}
	progress("media", "eject virtual media ok: "+summarizeEject(ejectRaw))

	if p.Reboot {
		progress("reboot", "ForceRestart")
		if _, err := s.agent.Reset(srv, map[string]any{"reset_type": "ForceRestart"}); err != nil {
			return fmt.Errorf("post-boot ForceRestart: %w", err)
		}
		progress("reboot", "ForceRestart ok")
	}
	return nil
}

func summarizeEject(raw json.RawMessage) string {
	var body struct {
		Ejected []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			WasInserted bool   `json:"was_inserted"`
			Ejected     bool   `json:"ejected"`
			Method      string `json:"method"`
		} `json:"ejected"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Ejected) == 0 {
		if len(raw) > 0 {
			return string(raw)
		}
		return "(no slots)"
	}
	var parts []string
	for _, e := range body.Ejected {
		label := e.ID
		if e.Name != "" {
			label = e.Name
		}
		switch {
		case e.Method == "already-empty":
			parts = append(parts, label+":empty")
		case e.Ejected && e.WasInserted:
			parts = append(parts, label+":ejected("+e.Method+")")
		case e.Ejected:
			parts = append(parts, label+":cleared("+e.Method+")")
		default:
			parts = append(parts, label+":failed")
		}
	}
	return strings.Join(parts, ", ")
}

func (s *Server) handleJobProvisionPreflight(ctx context.Context, job *jobs.Job, progress jobs.Progress) error {
	var p provisionJobPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("bad payload: %w", err)
	}
	srv, err := s.store.GetServer(job.ServerID)
	if err != nil {
		return err
	}
	host, err := s.liveHost(srv)
	if err != nil {
		return err
	}
	req := provision.Request{ImageURL: p.ImageURL, TargetDisk: p.TargetDisk, Cidata: p.Cidata}
	var warnings []string
	err = provision.Preflight(ctx, host, req, func(phase, line string) {
		progress(phase, line)
		if strings.Contains(line, "WARN:") {
			warnings = append(warnings, line)
		}
	})
	result := map[string]any{"ok": err == nil, "warnings": warnings}
	if err != nil {
		result["error"] = err.Error()
		s.jobs.SetResult(job.ID, result)
		return err
	}
	s.jobs.SetResult(job.ID, result)
	return nil
}

func (s *Server) handleJobStorageDetect(ctx context.Context, job *jobs.Job, progress jobs.Progress) error {
	srv, err := s.store.GetServer(job.ServerID)
	if err != nil {
		return err
	}
	host, err := s.liveHost(srv)
	if err != nil {
		_ = s.saveDetect(srv.ID, storage.DetectResult{
			OK: false, Family: storage.FamilyUnknown, Error: err.Error(),
		})
		return err
	}
	progress("detect", "probing RAID tools over SSH")
	d := storage.Detect(ctx, host)
	if err := s.saveDetect(srv.ID, d); err != nil {
		return fmt.Errorf("detect ok but failed to save: %w", err)
	}
	result := map[string]any{
		"ok": d.OK, "family": d.Family, "tool": d.Tool, "model": d.Model,
		"slot": d.Slot, "data_ip": d.DataIP, "error": d.Error, "detail": d.Detail,
		"detected_at": d.DetectedAt,
	}
	s.jobs.SetResult(job.ID, result)
	if !d.OK || d.Family == storage.FamilyUnknown {
		msg := storage.FormatDetectError(d)
		if msg == "" {
			msg = "RAID detect failed"
		}
		return fmt.Errorf("%s", msg)
	}
	progress("detect", fmt.Sprintf("family=%s tool=%s", d.Family, d.Tool))
	return nil
}

func (s *Server) handleJobStorageConfig(ctx context.Context, job *jobs.Job, progress jobs.Progress) error {
	srv, err := s.store.GetServer(job.ServerID)
	if err != nil {
		return err
	}
	drv, host, det, err := s.driverForServer(srv)
	if err != nil {
		return err
	}
	progress("config", "reading controller config")
	cfg, err := drv.Config(ctx, host)
	if err != nil {
		return err
	}
	if cfg.Family == "" {
		cfg.Family = det.Family
	}
	if cfg.Tool == "" {
		cfg.Tool = det.Tool
	}
	_, _ = s.persistControllerLayout(srv.ID, cfg, host.IP)
	s.jobs.SetResult(job.ID, cfg)
	progress("config", "layout persisted")
	return nil
}

func (s *Server) handleJobStorageExec(ctx context.Context, job *jobs.Job, progress jobs.Progress) error {
	var p storageExecPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("bad payload: %w", err)
	}
	srv, err := s.store.GetServer(job.ServerID)
	if err != nil {
		return err
	}
	drv, host, _, err := s.driverForServer(srv)
	if err != nil {
		return err
	}
	if !drv.Allowed(p.Args) {
		return fmt.Errorf("command not allowed: %s", strings.Join(p.Args, " "))
	}
	progress("exec", strings.Join(p.Args, " "))
	out, err := drv.Exec(ctx, host, p.Args)
	result := map[string]any{"output": out, "tool": drv.Tool(), "family": string(drv.Family())}
	if err != nil {
		result["error"] = err.Error()
		s.jobs.SetResult(job.ID, result)
		// Include remote CLI text so UI/activity logs show Description=, not a bare failure.
		if tail := strings.TrimSpace(out); tail != "" {
			if len(tail) > 2500 {
				tail = tail[len(tail)-2500:]
			}
			return fmt.Errorf("%w\n--- remote ---\n%s", err, tail)
		}
		return err
	}
	if cfg, cerr := drv.Config(ctx, host); cerr == nil {
		_, _ = s.persistControllerLayout(srv.ID, cfg, host.IP)
	}
	s.jobs.SetResult(job.ID, result)
	return nil
}

func (s *Server) handleJobSoftRAIDScan(ctx context.Context, job *jobs.Job, progress jobs.Progress) error {
	srv, err := s.store.GetServer(job.ServerID)
	if err != nil {
		return err
	}
	host, err := s.liveHost(srv)
	if err != nil {
		_ = s.saveSoftRAID(srv.ID, softraid.Topology{OK: false, Error: err.Error()})
		return err
	}
	progress("scan", "probing mdadm over SSH")
	top := softraid.Scan(ctx, host)
	if err := s.saveSoftRAID(srv.ID, top); err != nil {
		return fmt.Errorf("scan ok but failed to save: %w", err)
	}
	lay := softraid.LayoutFromTopology(top, host.IP)
	_ = s.saveLayout(srv.ID, lay)
	s.jobs.SetResult(job.ID, top)
	if !top.OK {
		msg := top.Error
		if msg == "" {
			msg = "soft RAID scan failed"
		}
		return fmt.Errorf("%s", msg)
	}
	progress("scan", fmt.Sprintf("arrays=%d disks=%d tool=%s", len(top.Arrays), len(top.Disks), top.Tool))
	return nil
}

func (s *Server) handleJobSoftRAIDExec(ctx context.Context, job *jobs.Job, progress jobs.Progress) error {
	var p softRAIDExecPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("bad payload: %w", err)
	}
	srv, err := s.store.GetServer(job.ServerID)
	if err != nil {
		return err
	}
	host, err := s.liveHost(srv)
	if err != nil {
		return err
	}
	progress("exec", fmt.Sprintf("softraid %s", p.Action.Op))
	out, err := softraid.Exec(ctx, host, p.Action)
	result := map[string]any{"output": out, "op": p.Action.Op}
	if err != nil {
		result["error"] = err.Error()
		s.jobs.SetResult(job.ID, result)
		if tail := strings.TrimSpace(out); tail != "" {
			if len(tail) > 2500 {
				tail = tail[len(tail)-2500:]
			}
			return fmt.Errorf("%w\n--- remote ---\n%s", err, tail)
		}
		return err
	}
	top := softraid.Scan(ctx, host)
	_ = s.saveSoftRAID(srv.ID, top)
	lay := softraid.LayoutFromTopology(top, host.IP)
	_ = s.saveLayout(srv.ID, lay)
	result["topology"] = top
	s.jobs.SetResult(job.ID, result)
	progress("exec", "layout refreshed")
	return nil
}

func (s *Server) handleJobStorageWipePartitions(ctx context.Context, job *jobs.Job, progress jobs.Progress) error {
	srv, err := s.store.GetServer(job.ServerID)
	if err != nil {
		return err
	}
	host, err := s.liveHost(srv)
	if err != nil {
		return err
	}
	progress("wipe", "clearing partition tables on "+host.IP)
	if err := storage.WipeDiskPartitions(ctx, host, func(phase, line string) {
		progress(phase, line)
	}); err != nil {
		return err
	}
	s.jobs.SetResult(job.ID, map[string]any{"ok": true, "data_ip": host.IP})
	return nil
}

func (s *Server) handleJobRedfishProbe(ctx context.Context, job *jobs.Job, progress jobs.Progress) error {
	srv, err := s.store.GetServer(job.ServerID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(srv.Endpoint) == "" {
		return fmt.Errorf("no BMC endpoint yet — set an IP (Edit) before Probe")
	}
	progress("probe", "Redfish boot read")
	raw, err := s.agent.Probe(srv)
	if err != nil {
		return err
	}
	srv.Status = store.StatusBound
	if err := s.store.UpdateServer(*srv); err != nil {
		return fmt.Errorf("probe ok but failed to mark bound: %w", err)
	}
	var parsed any
	_ = json.Unmarshal(raw, &parsed)
	s.jobs.SetResult(job.ID, parsed)
	return nil
}

func (s *Server) handleJobRedfishInventory(ctx context.Context, job *jobs.Job, progress jobs.Progress) error {
	return s.runRedfishJob(job, progress, "inventory", true, func(srv *store.Server) (json.RawMessage, error) {
		progress("inventory", "fetching Redfish inventory")
		return s.agent.Inventory(srv)
	})
}

func (s *Server) handleJobRedfishReset(ctx context.Context, job *jobs.Job, progress jobs.Progress) error {
	resetType := "ForceRestart"
	if len(job.Payload) > 0 && string(job.Payload) != "null" {
		var p resetJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return fmt.Errorf("bad payload: %w", err)
		}
		if t := normalizeResetType(p.ResetType); t != "" {
			resetType = t
		}
	}
	return s.runRedfishJob(job, progress, "reset", false, func(srv *store.Server) (json.RawMessage, error) {
		progress("reset", resetType)
		return s.agent.Reset(srv, map[string]string{"reset_type": resetType})
	})
}

func (s *Server) handleJobInventoryClaimNICs(ctx context.Context, job *jobs.Job, progress jobs.Progress) error {
	var p claimNICsJobPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("bad payload: %w", err)
	}
	progress("claim", fmt.Sprintf("resolve lease %s (%s)", strings.TrimSpace(p.IP), strings.TrimSpace(p.MAC)))
	res, err := s.claimNICsFromLeaseIP(ctx, p.IP, p.MAC, progress)
	if err != nil {
		return err
	}
	s.jobs.SetResult(job.ID, res)
	// Stamp job with the matched server for Jobs UI (discovery jobs enqueue with ServerID=0).
	if res.ServerID > 0 {
		s.jobs.SetServerMeta(job.ID, res.ServerID, res.ServerName)
	}
	progress("claim", fmt.Sprintf("attached to %s (+%d MAC)", res.ServerName, res.Added))
	return nil
}

func (s *Server) handleJobRedfishConvertUEFI(ctx context.Context, job *jobs.Job, progress jobs.Progress) error {
	reboot := true
	if len(job.Payload) > 0 && string(job.Payload) != "null" {
		var p convertUEFIJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return fmt.Errorf("bad payload: %w", err)
		}
		if p.Reboot != nil {
			reboot = *p.Reboot
		}
	}
	body := map[string]any{"reboot": reboot}
	return s.runRedfishJob(job, progress, "convert-uefi", false, func(srv *store.Server) (json.RawMessage, error) {
		progress("bios", fmt.Sprintf("stage persistent BIOS boot mode → UEFI (reboot=%v)", reboot))
		raw, err := s.agent.ConvertToUEFI(srv, body)
		if err != nil {
			return nil, err
		}
		var res struct {
			Attribute     string `json:"attribute"`
			PreviousValue string `json:"previous_value"`
			PendingValue  string `json:"pending_value"`
			AlreadyUEFI   bool   `json:"already_uefi"`
			Patched       bool   `json:"patched"`
			Rebooted      bool   `json:"rebooted"`
			Note          string `json:"note"`
		}
		if json.Unmarshal(raw, &res) == nil {
			progress("bios", fmt.Sprintf("%s %s→%s already=%v patched=%v rebooted=%v %s",
				res.Attribute, res.PreviousValue, res.PendingValue, res.AlreadyUEFI, res.Patched, res.Rebooted, res.Note))
		}
		return raw, nil
	})
}

func (s *Server) handleJobRedfishMount(ctx context.Context, job *jobs.Job, progress jobs.Progress) error {
	var p mountJobPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("bad payload: %w", err)
	}
	payload := map[string]any{
		"image_url": p.ImageURL,
		"boot_once": p.BootOnce,
		"reboot":    p.Reboot,
	}
	if err := s.runRedfishJob(job, progress, "mount", false, func(srv *store.Server) (json.RawMessage, error) {
		progress("mount", p.ImageURL)
		return s.agent.MountMedia(srv, payload)
	}); err != nil {
		return err
	}
	if !p.Reboot {
		progress("live-os", "skip NIC claim (reboot not requested)")
		return nil
	}
	srv, err := s.store.GetServer(job.ServerID)
	if err != nil {
		return err
	}
	progress("live-os", "claim data NICs via known/orphan DHCP leases (≤2 SSH tries per IP)")
	added, claimErr := s.waitAndClaimLiveOSNICs(ctx, srv, progress)
	if claimErr != nil {
		// Mount/reboot already succeeded — keep job green but surface the miss.
		progress("live-os", "NIC claim incomplete: "+claimErr.Error())
		return nil
	}
	if added == 0 {
		progress("live-os", "reachable; inventory already had all live NICs")
		return nil
	}
	progress("live-os", fmt.Sprintf("inventory updated with %d new NIC MAC(s)", added))
	return nil
}

func (s *Server) runRedfishJob(job *jobs.Job, progress jobs.Progress, kind string, persist bool, fn func(*store.Server) (json.RawMessage, error)) error {
	srv, err := s.store.GetServer(job.ServerID)
	if err != nil {
		return err
	}
	if srv.Status == store.StatusDeclared {
		return fmt.Errorf("server is still declared — Probe (or wait for OOB DHCP bind) before Redfish actions")
	}
	if strings.TrimSpace(srv.Endpoint) == "" {
		return fmt.Errorf("no BMC endpoint yet")
	}
	raw, err := fn(srv)
	if err != nil {
		return err
	}
	formatted := string(raw)
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err == nil {
		if pretty, err := json.MarshalIndent(parsed, "", "  "); err == nil {
			formatted = string(pretty)
		}
	}
	if persist {
		if err := s.store.SaveSnapshot(srv.ID, kind, formatted); err != nil {
			return fmt.Errorf("ok but failed to cache: %w", err)
		}
		if kind == "inventory" {
			if ok, err := s.applyChassisIDFromInventory(srv, formatted); err != nil {
				return fmt.Errorf("inventory cached but chassis_id update failed: %w", err)
			} else if ok {
				progress("inventory", "chassis_id ← system.serial_number "+srv.ChassisID)
			}
		}
	}
	s.jobs.SetResult(job.ID, parsed)
	progress(kind, "ok")
	return nil
}

func (s *Server) enqueueJob(opts jobs.EnqueueOpts) (*jobs.Job, error) {
	if s.jobs == nil {
		return nil, fmt.Errorf("job registry not available")
	}
	s.jobs.SetExecutors(s.provisionExecutors())
	return s.jobs.Enqueue(opts)
}

// withServerLock serializes host/BMC work with jobs for the same ServerID.
// Required for any sync path that touches a baremetal outside Enqueue.
func (s *Server) withServerLock(ctx context.Context, serverID int64, fn func(context.Context) error) error {
	if s.jobs == nil {
		return fmt.Errorf("job registry not available")
	}
	return s.jobs.WithServer(ctx, serverID, fn)
}
