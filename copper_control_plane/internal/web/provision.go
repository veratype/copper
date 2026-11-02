package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"copper_control_plane/internal/jobs"
	"copper_control_plane/internal/provision"
	"copper_control_plane/internal/softraid"
	"copper_control_plane/internal/storage"
	"copper_control_plane/internal/store"
)

func (s *Server) loadLayout(serverID int64) (storage.Layout, error) {
	snap, err := s.store.GetSnapshot(serverID, storage.LayoutSnapshotKind)
	if err != nil {
		return storage.Layout{}, err
	}
	if snap == nil || strings.TrimSpace(snap.Payload) == "" {
		return storage.Layout{}, nil
	}
	var lay storage.Layout
	if err := json.Unmarshal([]byte(snap.Payload), &lay); err != nil {
		return storage.Layout{}, err
	}
	return lay, nil
}

func (s *Server) saveLayout(serverID int64, lay storage.Layout) error {
	raw, err := json.MarshalIndent(lay, "", "  ")
	if err != nil {
		return err
	}
	return s.store.SaveSnapshot(serverID, storage.LayoutSnapshotKind, string(raw))
}

func (s *Server) persistControllerLayout(serverID int64, cfg storage.Controller, dataIP string) (storage.Layout, error) {
	lay := storage.LayoutFromController(cfg, dataIP)
	if err := s.saveLayout(serverID, lay); err != nil {
		return lay, err
	}
	return lay, nil
}

func (s *Server) provisionTimeout() time.Duration {
	rt, err := s.store.GetBatchRuntime()
	if err != nil {
		return time.Duration(store.DefaultTimeoutProvisionSeconds) * time.Second
	}
	sec := rt.Timeouts.Provision
	if sec < 120 {
		sec = store.DefaultTimeoutProvisionSeconds
	}
	return time.Duration(sec) * time.Second
}

func (s *Server) provisionExecutors() int {
	rt, err := s.store.GetBatchRuntime()
	if err != nil {
		return store.DefaultBatchExecutors
	}
	n := rt.Executors
	if n < 1 {
		n = store.DefaultBatchExecutors
	}
	if n > store.MaxBatchExecutors {
		n = store.MaxBatchExecutors
	}
	return n
}

func (s *Server) preflightTimeout() time.Duration {
	// Preflight is SSH + curl probe; reuse detect timeout band, floor 60s.
	rt, err := s.store.GetBatchRuntime()
	sec := store.DefaultTimeoutDetectSeconds
	if err == nil && rt.Timeouts.Detect >= 30 {
		sec = rt.Timeouts.Detect
	}
	if sec < 60 {
		sec = 60
	}
	if sec > 300 {
		sec = 300
	}
	return time.Duration(sec) * time.Second
}

func (s *Server) handleAPIDataMediaFiles(w http.ResponseWriter, _ *http.Request) {
	if s.dataMedia == nil {
		s.writeAPIJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "data media not configured"})
		return
	}
	cat, err := s.dataMedia.ListCatalog()
	if err != nil {
		s.writeAPIJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "catalog": cat})
		return
	}
	st := s.dataMedia.Status()
	s.writeAPIJSON(w, http.StatusOK, map[string]any{
		"ok": true, "catalog": cat, "status": st,
	})
}

func (s *Server) handleAPILayoutGet(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		s.writeAPIJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	lay, err := s.loadLayout(srv.ID)
	if err != nil {
		s.writeAPIJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if lay.Fingerprint == "" && !lay.OK {
		s.writeAPIJSON(w, http.StatusConflict, map[string]any{
			"ok": false, "error": "no persisted storage layout — open Storage and load/save config first (or pin direct disks)",
		})
		return
	}
	// Rebind volatile /dev paths from live lsblk (WWN/Unique ID) so Deploy shows current letters.
	if lay.Mode == "raid" && lay.Controller != nil {
		if host, herr := s.liveHost(srv); herr == nil {
			_ = s.withServerLock(r.Context(), srv.ID, func(ctx context.Context) error {
				cctx, cancel := context.WithTimeout(ctx, s.detectTimeout())
				defer cancel()
				disks, derr := storage.ListBlockDisks(cctx, host)
				if derr != nil {
					return derr
				}
				changed := storage.RefreshLayoutOSDisks(&lay, disks)
				if host.IP != "" && lay.DataIP != host.IP {
					lay.DataIP = host.IP
					changed = true
				}
				if changed {
					_ = s.saveLayout(srv.ID, lay)
				}
				return nil
			})
		}
	}
	if lay.Mode == "soft_raid" {
		if host, herr := s.liveHost(srv); herr == nil {
			_ = s.withServerLock(r.Context(), srv.ID, func(ctx context.Context) error {
				cctx, cancel := context.WithTimeout(ctx, s.detectTimeout())
				defer cancel()
				top := softraid.Scan(cctx, host)
				if !top.OK {
					return nil
				}
				_ = s.saveSoftRAID(srv.ID, top)
				fresh := softraid.LayoutFromTopology(top, host.IP)
				lay = fresh
				_ = s.saveLayout(srv.ID, lay)
				return nil
			})
		}
	}
	// Always expose Copper Data DHCP's current lease — never a stale layout DataIP.
	if hip, _, e := s.dataIPForServer(srv); e == nil {
		lay.DataIP = hip
	} else {
		lay.DataIP = ""
	}
	s.writeAPIJSON(w, http.StatusOK, map[string]any{"ok": true, "layout": lay})
}

// resolveHostTargetDisk maps the Deploy selection to this host's live /dev path.
// Selection is matched by stable target key (array:A:1 / drive:…) then DeviceID → WWN.
func (s *Server) resolveHostTargetDisk(ctx context.Context, host storage.Host, lay storage.Layout, selected, key string) string {
	selected = strings.TrimSpace(selected)
	key = strings.TrimSpace(key)
	var t storage.InstallTarget
	var ok bool
	if key != "" {
		t, ok = storage.FindTargetByKey(lay, key)
	}
	if !ok {
		t, ok = storage.FindTargetByDevice(lay, selected)
		if ok {
			key = t.Key
		}
	}
	if lay.Mode == "soft_raid" {
		if ok {
			if path := softraid.ResolveArrayPath(ctx, host, t); path != "" {
				return path
			}
			if strings.TrimSpace(t.Device) != "" {
				return strings.TrimSpace(t.Device)
			}
		}
		return selected
	}
	disks, err := storage.ListBlockDisks(ctx, host)
	if err != nil {
		if ok && strings.TrimSpace(t.Device) != "" {
			return strings.TrimSpace(t.Device)
		}
		return selected
	}
	if lay.Mode == "raid" && lay.Controller != nil {
		storage.RefreshLayoutOSDisks(&lay, disks)
		if key != "" {
			if t2, ok2 := storage.FindTargetByKey(lay, key); ok2 {
				t, ok = t2, true
			}
		}
	}
	if ok {
		if path := storage.ResolveInstallPath(disks, t); path != "" {
			return path
		}
	}
	return selected
}

func (s *Server) targetKeyFromLayout(lay storage.Layout, selected string) string {
	if t, ok := storage.FindTargetByDevice(lay, selected); ok {
		return t.Key
	}
	// Fall back: first array / md target (common Deploy default).
	for _, t := range lay.Targets {
		if (t.Source == "array" || t.Source == "md") && strings.TrimSpace(t.Key) != "" {
			return t.Key
		}
	}
	return ""
}

func (s *Server) handleAPILayoutPinDirect(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		s.writeAPIJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	host, err := s.liveHost(srv)
	if err != nil {
		s.writeAPIJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var lay storage.Layout
	err = s.withServerLock(r.Context(), srv.ID, func(ctx context.Context) error {
		cctx, cancel := context.WithTimeout(ctx, s.detectTimeout())
		defer cancel()
		disks, derr := storage.ListBlockDisks(cctx, host)
		if derr != nil {
			return derr
		}
		lay = storage.LayoutFromDisks(disks, host.IP)
		return s.saveLayout(srv.ID, lay)
	})
	if err != nil {
		s.writeAPIJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeAPIJSON(w, http.StatusOK, map[string]any{"ok": true, "layout": lay})
}

func (s *Server) handleAPIHostDisks(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		s.writeAPIJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	host, err := s.liveHost(srv)
	if err != nil {
		s.writeAPIJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var disks []storage.BlockDisk
	err = s.withServerLock(r.Context(), srv.ID, func(ctx context.Context) error {
		cctx, cancel := context.WithTimeout(ctx, s.detectTimeout())
		defer cancel()
		var derr error
		disks, derr = storage.ListBlockDisks(cctx, host)
		return derr
	})
	if err != nil {
		s.writeAPIJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeAPIJSON(w, http.StatusOK, map[string]any{"ok": true, "disks": disks, "data_ip": host.IP})
}

func (s *Server) handleAPIHostNICs(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		s.writeAPIJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	host, err := s.liveHost(srv)
	if err != nil {
		s.writeAPIJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var nics []storage.NICInfo
	err = s.withServerLock(r.Context(), srv.ID, func(ctx context.Context) error {
		cctx, cancel := context.WithTimeout(ctx, s.detectTimeout())
		defer cancel()
		var nerr error
		nics, nerr = storage.ListNICs(cctx, host)
		return nerr
	})
	if err != nil {
		s.writeAPIJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeAPIJSON(w, http.StatusOK, map[string]any{"ok": true, "nics": nics, "data_ip": host.IP})
}

type provisionStartBody struct {
	IDs           []int64          `json:"ids"`
	ImagePath     string           `json:"image_path"` // relative path in data media folder
	ImageURL      string           `json:"image_url"`  // optional override
	TargetDisk    string           `json:"target_disk"`
	Cidata        provision.Cidata `json:"cidata"`
	NICSelect     string           `json:"nic_select"` // by_name | data_nic
	SetBootHDD    bool             `json:"set_boot_hdd"`
	Reboot        bool             `json:"reboot"`
	PreflightOnly bool             `json:"preflight_only"` // check all hosts; do not write
	ReuseImage    bool             `json:"reuse_image"`    // retry only: skip rewrite if checksum matches
}

type provisionPending struct {
	srv  *store.Server
	host storage.Host
	lay  storage.Layout
}

type preflightHostResult struct {
	ServerID   int64    `json:"server_id"`
	ServerName string   `json:"server_name"`
	OK         bool     `json:"ok"`
	Error      string   `json:"error,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	Log        []string `json:"log,omitempty"`
}

func (s *Server) resolveImageURL(pathOrURL string) (string, error) {
	pathOrURL = strings.TrimSpace(pathOrURL)
	if pathOrURL == "" {
		return "", fmt.Errorf("image_path or image_url required")
	}
	if strings.HasPrefix(pathOrURL, "http://") || strings.HasPrefix(pathOrURL, "https://") {
		return pathOrURL, nil
	}
	if s.dataMedia == nil {
		return "", fmt.Errorf("data media server not available")
	}
	st := s.dataMedia.Status()
	if !st.Running {
		return "", fmt.Errorf("data media server is not running — enable it in Settings")
	}
	u := s.dataMedia.FileURL(pathOrURL)
	if u == "" {
		return "", fmt.Errorf("cannot build image URL (set a concrete Bind IP for data media in Settings)")
	}
	return u, nil
}

// resolveImageDigest returns sha256+size for a media library path or our data-media URL.
// Empty digest is non-fatal — provision just always rewrites.
func (s *Server) resolveImageDigest(pathOrURL string) (sha256hex string, size int64) {
	if s.dataMedia == nil {
		return "", 0
	}
	pathOrURL = strings.TrimSpace(pathOrURL)
	rel := pathOrURL
	if strings.HasPrefix(pathOrURL, "http://") || strings.HasPrefix(pathOrURL, "https://") {
		base := strings.TrimRight(s.dataMedia.Status().BaseURL, "/")
		if base == "" || !strings.HasPrefix(pathOrURL, base+"/") {
			return "", 0
		}
		rel = strings.TrimPrefix(pathOrURL, base+"/")
	}
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "", 0
	}
	d, err := s.dataMedia.FileDigest(rel)
	if err != nil || d.SHA256 == "" {
		return "", 0
	}
	return d.SHA256, d.Size
}

func (s *Server) resolveProvisionPending(ids []int64) ([]provisionPending, error) {
	var baseFP string
	pend := make([]provisionPending, 0, len(ids))
	for i, id := range ids {
		srv, err := s.store.GetServer(id)
		if err != nil {
			return nil, err
		}
		lay, err := s.loadLayout(id)
		if err != nil {
			return nil, err
		}
		if lay.Fingerprint == "" && lay.BatchFingerprint == "" {
			return nil, fmt.Errorf("%s: no persisted storage layout", srv.Name)
		}
		fp := lay.BatchFingerprint
		if fp == "" {
			fp = lay.Fingerprint
		}
		if i == 0 {
			baseFP = fp
		} else if fp != baseFP {
			return nil, fmt.Errorf("HDD layouts differ — batch provision requires identical layouts (%s vs %s)",
				pend[0].srv.Name, srv.Name)
		}
		host, err := s.liveHost(srv)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", srv.Name, err)
		}
		pend = append(pend, provisionPending{srv: srv, host: host, lay: lay})
	}
	return pend, nil
}

func (s *Server) runBatchPreflight(pend []provisionPending, imageURL, target, targetKey string, perHost []provision.Cidata) []preflightHostResult {
	executors := s.provisionExecutors()
	timeout := s.preflightTimeout()
	results := make([]preflightHostResult, len(pend))
	sem := make(chan struct{}, executors)
	var wg sync.WaitGroup
	for i, p := range pend {
		wg.Add(1)
		go func(i int, p provisionPending) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			c := perHost[i]
			res := preflightHostResult{ServerID: p.srv.ID, ServerName: p.srv.Name, OK: true}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			err := s.withServerLock(ctx, p.srv.ID, func(locked context.Context) error {
				disk := s.resolveHostTargetDisk(locked, p.host, p.lay, target, targetKey)
				req := provision.Request{ImageURL: imageURL, TargetDisk: disk, Cidata: c}
				return provision.Preflight(locked, p.host, req, func(phase, line string) {
					line = strings.TrimSpace(line)
					if line == "" {
						return
					}
					res.Log = append(res.Log, fmt.Sprintf("[%s] %s", phase, line))
					if strings.Contains(line, "WARN:") {
						res.Warnings = append(res.Warnings, line)
					}
				})
			})
			if err != nil {
				res.OK = false
				res.Error = err.Error()
			}
			results[i] = res
		}(i, p)
	}
	wg.Wait()
	return results
}

func (s *Server) handleAPIProvisionStart(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		s.writeAPIJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "jobs not available"})
		return
	}
	var body provisionStartBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad json"})
		return
	}
	ids := body.IDs
	if idStr := r.PathValue("id"); idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad server id"})
			return
		}
		ids = []int64{id}
	}
	if len(ids) == 0 {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ids required"})
		return
	}

	imgSrc := body.ImageURL
	if imgSrc == "" {
		imgSrc = body.ImagePath
	}
	imageURL, err := s.resolveImageURL(imgSrc)
	if err != nil {
		s.writeAPIJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	imageSHA, imageSize := s.resolveImageDigest(imgSrc)
	if imageSHA == "" {
		imageSHA, imageSize = s.resolveImageDigest(imageURL)
	}
	target := strings.TrimSpace(body.TargetDisk)
	if target == "" {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "target_disk required"})
		return
	}
	cidata := body.Cidata.Normalize()
	if err := cidata.ValidateCredentials(); err != nil {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	nicSelect := normalizeNICSelect(body.NICSelect)

	pend, err := s.resolveProvisionPending(ids)
	if err != nil {
		s.writeAPIJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	executors := s.provisionExecutors()
	s.jobs.SetExecutors(executors)

	resolveCtx, resolveCancel := context.WithTimeout(r.Context(), s.detectTimeout())
	perHost, err := s.expandAllHostCidata(resolveCtx, pend, cidata, nicSelect)
	resolveCancel()
	if err != nil {
		s.writeAPIJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	type started struct {
		ServerID   int64  `json:"server_id"`
		ServerName string `json:"server_name"`
		JobID      string `json:"job_id"`
		Hostname   string `json:"hostname,omitempty"`
		Interface  string `json:"interface,omitempty"`
		MAC        string `json:"mac,omitempty"`
		Address    string `json:"address_cidr,omitempty"`
	}
	type hostPlan struct {
		ServerID   int64  `json:"server_id"`
		ServerName string `json:"server_name"`
		Hostname   string `json:"hostname"`
		Interface  string `json:"interface"`
		MAC        string `json:"mac"`
		Address    string `json:"address_cidr,omitempty"`
	}
	plan := make([]hostPlan, len(pend))
	for i, p := range pend {
		plan[i] = hostPlan{
			ServerID: p.srv.ID, ServerName: p.srv.Name,
			Hostname: perHost[i].Hostname, Interface: perHost[i].Interface,
			MAC: perHost[i].MAC, Address: perHost[i].AddressCIDR,
		}
	}

	targetKey := ""
	if len(pend) > 0 {
		targetKey = s.targetKeyFromLayout(pend[0].lay, target)
	}

	if body.PreflightOnly {
		timeout := s.preflightTimeout()
		var startedJobs []started
		for i, p := range pend {
			c := perHost[i]
			var hostDisk string
			err := s.withServerLock(r.Context(), p.srv.ID, func(ctx context.Context) error {
				diskCtx, diskCancel := context.WithTimeout(ctx, s.detectTimeout())
				defer diskCancel()
				hostDisk = s.resolveHostTargetDisk(diskCtx, p.host, p.lay, target, targetKey)
				return nil
			})
			if err != nil {
				s.writeAPIJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			job, err := s.enqueueJob(jobs.EnqueueOpts{
				Type:       jobs.TypeProvisionPreflight,
				ServerID:   p.srv.ID,
				ServerName: p.srv.Name,
				Timeout:    timeout,
				ImageURL:   imageURL,
				TargetDisk: hostDisk,
				Payload: provisionJobPayload{
					ImageURL: imageURL, ImageSHA256: imageSHA, ImageSize: imageSize,
					ReuseImage: body.ReuseImage, TargetDisk: hostDisk, Cidata: c,
				},
			})
			if err != nil {
				s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			startedJobs = append(startedJobs, started{
				ServerID: p.srv.ID, ServerName: p.srv.Name, JobID: job.ID,
				Hostname: c.Hostname, Interface: c.Interface, MAC: c.MAC, Address: c.AddressCIDR,
			})
		}
		s.writeAPIJSON(w, http.StatusAccepted, map[string]any{
			"ok": true, "preflight_only": true, "jobs": startedJobs, "plan": plan,
			"executors": executors, "image_url": imageURL, "target_disk": target,
			"image_sha256": imageSHA, "reuse_image": body.ReuseImage,
			"target_key": targetKey, "nic_select": nicSelect,
		})
		return
	}

	// Gate: every host must pass preflight before any write starts.
	pf := s.runBatchPreflight(pend, imageURL, target, targetKey, perHost)
	failN := 0
	for _, r := range pf {
		if !r.OK {
			failN++
		}
	}
	if failN > 0 {
		s.writeAPIJSON(w, http.StatusConflict, map[string]any{
			"ok": false,
			"error": fmt.Sprintf("preflight failed on %d/%d host(s) — no writes started", failN, len(pf)),
			"preflight": pf, "failed": failN, "executors": executors, "plan": plan,
			"image_url": imageURL, "target_disk": target, "target_key": targetKey,
		})
		return
	}

	timeout := s.provisionTimeout()
	if body.SetBootHDD || body.Reboot {
		timeout += 2 * time.Minute
	}
	var startedJobs []started
	for i, p := range pend {
		c := perHost[i]
		var hostDisk string
		err := s.withServerLock(r.Context(), p.srv.ID, func(ctx context.Context) error {
			diskCtx, diskCancel := context.WithTimeout(ctx, s.detectTimeout())
			defer diskCancel()
			hostDisk = s.resolveHostTargetDisk(diskCtx, p.host, p.lay, target, targetKey)
			return nil
		})
		if err != nil {
			s.writeAPIJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		job, err := s.enqueueJob(jobs.EnqueueOpts{
			Type:       jobs.TypeProvisionWrite,
			ServerID:   p.srv.ID,
			ServerName: p.srv.Name,
			Timeout:    timeout,
			ImageURL:   imageURL,
			TargetDisk: hostDisk,
			Payload: provisionJobPayload{
				ImageURL: imageURL, ImageSHA256: imageSHA, ImageSize: imageSize,
				ReuseImage: body.ReuseImage, TargetDisk: hostDisk, Cidata: c,
				SkipPreflight: true, // batch gate already passed
				SetBootHDD:    body.SetBootHDD,
				Reboot:        body.Reboot,
			},
		})
		if err != nil {
			s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		startedJobs = append(startedJobs, started{
			ServerID: p.srv.ID, ServerName: p.srv.Name, JobID: job.ID,
			Hostname: c.Hostname, Interface: c.Interface, MAC: c.MAC, Address: c.AddressCIDR,
		})
	}

	s.writeAPIJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "jobs": startedJobs, "plan": plan, "image_url": imageURL, "target_disk": target,
		"image_sha256": imageSHA, "reuse_image": body.ReuseImage,
		"target_key": targetKey, "preflight": pf, "executors": executors, "nic_select": nicSelect,
	})
}

func (s *Server) handleDeployPage(w http.ResponseWriter, r *http.Request) {
	ids, err := parseStorageIDs(r)
	if err != nil || len(ids) == 0 {
		http.Error(w, "need server id(s)", http.StatusBadRequest)
		return
	}
	type srvMeta struct {
		ID               int64    `json:"id"`
		Name             string   `json:"name"`
		Fingerprint      string   `json:"fingerprint"`
		BatchFingerprint string   `json:"batch_fingerprint,omitempty"`
		Mode             string   `json:"mode"`
		Family           string   `json:"family"`
		OK               bool     `json:"ok"`
		TargetHint       string   `json:"target_hint"`
		DataIP           string   `json:"data_ip"`
		Labels           []string `json:"labels,omitempty"`
		// Inventory provisioned OS network — preferred over Deploy form when set.
		ProvisionedHostname string `json:"provisioned_hostname,omitempty"`
		ProvisionedIPCIDR   string `json:"provisioned_ip_cidr,omitempty"`
		ProvisionedGW       string `json:"provisioned_gw,omitempty"`
		ProvisionedDNS      string `json:"provisioned_dns,omitempty"`
	}
	metas := make([]srvMeta, 0, len(ids))
	for _, id := range ids {
		srv, err := s.store.GetServer(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		lay, _ := s.loadLayout(id)
		hint := ""
		for _, t := range lay.Targets {
			if (t.Source == "array" || t.Source == "md") && strings.TrimSpace(t.Device) != "" {
				hint = t.Device
				break
			}
		}
		if hint == "" {
			for _, t := range lay.Targets {
				if strings.TrimSpace(t.Device) != "" {
					hint = t.Device
					break
				}
			}
		}
		// Live Data DHCP only — never the persisted layout's DataIP.
		ip := ""
		if hip, _, e := s.dataIPForServer(srv); e == nil {
			ip = hip
		}
		batchFP := lay.BatchFingerprint
		if batchFP == "" {
			batchFP = lay.Fingerprint
		}
		metas = append(metas, srvMeta{
			ID: srv.ID, Name: srv.Name, Fingerprint: lay.Fingerprint,
			BatchFingerprint: batchFP,
			Mode: lay.Mode, Family: string(lay.Family), OK: lay.OK || lay.Fingerprint != "",
			TargetHint: hint, DataIP: ip, Labels: srv.Labels,
			ProvisionedHostname: srv.ProvisionedHostname,
			ProvisionedIPCIDR:   srv.ProvisionedIPCIDR,
			ProvisionedGW:       srv.ProvisionedGW,
			ProvisionedDNS:      srv.ProvisionedDNS,
		})
	}
	var mediaFiles []mediaFileRow
	var mediaBase, mediaNote, mediaErr string
	if s.dataMedia != nil {
		cat, err := s.dataMedia.ListCatalog()
		if err != nil {
			mediaErr = err.Error()
		} else {
			mediaBase = cat.BaseURL
			mediaNote = cat.Note
			for _, f := range cat.Files {
				u := ""
				if mediaBase != "" {
					u = strings.TrimRight(mediaBase, "/") + "/" + f.Path
				}
				mediaFiles = append(mediaFiles, mediaFileRow{
					Path: f.Path, SizeHuman: formatByteSize(f.Size), URL: u,
				})
			}
		}
		if !s.dataMedia.Status().Running {
			if mediaNote == "" {
				mediaNote = "Data media server is not running — enable it under Settings"
			}
		}
	} else {
		mediaErr = "data media not wired"
	}

	idsJSON, _ := json.Marshal(ids)
	metaJSON, _ := json.Marshal(metas)
	primary := metas[0]
	s.render(w, "deploy.html", pageData{
		Title:        "Deploy OS · " + primary.Name,
		Authed:       true,
		ServerIDs:    template.JS(idsJSON),
		StorageMeta:  template.JS(metaJSON),
		MediaFiles:   mediaFiles,
		MediaBaseURL: mediaBase,
		MediaNote:    mediaNote,
		MediaError:   mediaErr,
		DataMedia:    s.storeDataMediaOrDefault(),
	})
}

func (s *Server) storeDataMediaOrDefault() store.DataMedia {
	dm, err := s.store.GetDataMedia()
	if err != nil {
		return store.DefaultDataMedia()
	}
	return dm
}
