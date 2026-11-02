package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"copper_control_plane/internal/activity"
	"copper_control_plane/internal/agent"
	"copper_control_plane/internal/config"
	"copper_control_plane/internal/datamedia"
	"copper_control_plane/internal/jobs"
	"copper_control_plane/internal/store"

	"copper_dhcp"
)

type Server struct {
	cfg       *config.Config
	store     *store.Store
	agent     *agent.Client
	localDHCP *copper_dhcp.Manager // data-plane DHCP running in this process
	localAct  *activity.Buffer     // local (data DHCP) activity ring
	dataMedia *datamedia.Manager   // data-plane cloud-image / ISO HTTP server
	jobs      *jobs.Registry
	pages     map[string]*template.Template
	staticFS  fs.FS
	staticVer string // short hash of app.js for cache-busting ?v=
	watch     agentWatchState

	// claimTakenMu guards claimTakenByIP: serverID → lease IP that already
	// claimed that baremetal during Resolve. Other orphan leases omit those
	// servers from the candidate search list (1 IP ↔ 1 baremetal per wave).
	claimTakenMu   sync.Mutex
	claimTakenByIP map[int64]string
}

type pageData struct {
	Title           string
	Authed          bool
	Flash           string
	Error           string
	AgentURL        string
	AgentMode       string // listen | poll
	AgentLastSeen   string // poll: RFC3339 as_of from last heartbeat
	AgentOS         string // poll: linux|windows|darwin from heartbeat
	Servers         []store.Server
	Server          *store.Server
	AllLabels       []string          // catalog of known labels (filter chips / datalist)
	FilterLabels    []string          // active label filters (AND)
	FilterLabelSet  map[string]bool   // lower(name) → selected, for chip UI
	FilterStatus    string
	FilterQ         string
	FilterActive    bool
	Editing         bool // server form is an existing row; false on add (including validation re-render)
	Result          string
	ResultKind      string // inventory | boot | virtual-media | reset | mount
	ResultFetchedAt string // RFC3339 / display time for cached snapshots

	DHCP             copper_dhcp.Config
	DHCPRole         bool
	DHCPRunning      bool
	AgentError       string
	AgentWatchNote   string // background auto-repush / reachability status
	DNSJoined        string
	AllowMACsText    string // extra MACs not tied to inventory servers
	DenyMACsText     string
	ReservationsText string
	LeasesJSON       string
	Leases           []leaseRow // structured agent/local leases for UI tables
	KnownMACs        []knownMAC // inventory servers with a MAC, for DHCP allow checkboxes
	HostServers      []hostServerGroup // Data DHCP: NICs grouped by server (accordion)
	HostNICCount     int
	HostBoundCount   int // servers with at least one live lease

	// CSV import
	ImportResult *csvImportResult
	CSVSample    string

	// Mount ISO: agent OOB media library
	MediaFiles   []mediaFileRow
	MediaBaseURL string
	MediaFolder  string
	MediaNote    string
	MediaError   string

	BatchRuntime store.BatchRuntime
	LiveOSSSH    store.LiveOSSSH
	DataMedia    store.DataMedia
	Session      store.SessionSettings

	// Storage console (live-OS RAID) / Deploy
	ServerIDs   template.JS      // JSON array of server ids
	StorageMeta template.JS      // JSON array of {id,name,family,...}
	RaidFamily  map[int64]string // servers page: last detect family per id
	DataIPs     map[int64]string // servers page: live Data DHCP lease IP per id
	DataIP      string           // server detail: this server's data-plane lease IP
	// SoftRAIDLevels is JSON array of mdadm levels for softraid.html.
	SoftRAIDLevels template.JS

	// Next is a same-origin return path for the login form (?next=/storage…).
	Next string

	// StaticVersion cache-busts /static/app.js so Storage/Deploy always get CopperJobs.
	StaticVersion string
}

// mediaFileRow is one agent media-folder entry for the mount UI.
type mediaFileRow struct {
	Path      string
	SizeHuman string
	URL       string // full BMC-reachable URL when MediaBaseURL is known
}

// knownMAC is a MAC offered on a DHCP allow list (OOB: BMC MAC; Data: host NIC).
type knownMAC struct {
	Name      string
	MAC       string
	Selected  bool
	NIC       string // host NIC name from inventory (Data DHCP)
	Hardware  string // manufacturer / model / serial (Data DHCP)
	ReserveIP string // existing reservation IP for this MAC (Data DHCP)
}

// hostNICRow is one host NIC inside a Data DHCP server accordion group.
type hostNICRow struct {
	MAC       string
	NIC       string
	Selected  bool
	ReserveIP string
	LeaseIP   string // live DHCP lease for this MAC, if any
	HasLease  bool
}

// hostServerGroup collapses many NICs under one inventory server.
type hostServerGroup struct {
	ID           int64 // inventory server id (0 for orphan group)
	Name         string
	Hardware     string
	NICs         []hostNICRow
	NICCount     int
	AllowedCount int
	LeaseCount   int
	LeaseSummary string // e.g. "10.0.0.12, 10.0.0.13"
	Open         bool   // expand when server has a live lease
	HasLease     bool
}

// leaseRow is one DHCP MAC→IP binding for templates / JSON.
type leaseRow struct {
	MAC        string `json:"mac"`
	IP         string `json:"ip"`
	ExpiresAt  string `json:"expires_at"`
	Reserved   bool   `json:"reserved"`
	ServerID   int64  `json:"server_id,omitempty"`
	ServerName string `json:"server_name,omitempty"` // matched inventory server (Data DHCP)
	Hardware   string `json:"hardware,omitempty"`
	NIC        string `json:"nic,omitempty"`
}

func New(cfg *config.Config, st *store.Store, ag *agent.Client, localDHCP *copper_dhcp.Manager, localAct *activity.Buffer, dataMedia *datamedia.Manager, jobReg *jobs.Registry, templatesFS, staticFS fs.FS) (*Server, error) {
	pages := map[string]*template.Template{}
	for _, name := range []string{"login.html", "servers.html", "server_form.html", "server_detail.html", "server_import.html", "dhcp.html", "dhcp_data.html", "activity.html", "jobs.html", "settings.html", "storage.html", "softraid.html", "deploy.html"} {
		t, err := template.ParseFS(templatesFS, "templates/layout.html", "templates/partials/*.html", "templates/"+name)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		pages[name] = t
	}
	if localAct == nil {
		localAct = activity.New(0)
	}
	if dataMedia == nil {
		dataMedia = datamedia.New(localAct)
	}
	if jobReg == nil {
		jobReg = jobs.New(st, localAct)
	}
	s := &Server{
		cfg: cfg, store: st, agent: ag, localDHCP: localDHCP, localAct: localAct,
		dataMedia: dataMedia, jobs: jobReg, pages: pages, staticFS: staticFS,
		staticVer: staticAssetVersion(staticFS),
	}
	s.registerJobHandlers()
	return s, nil
}

// staticAssetVersion returns a short content hash of static assets for ?v= cache busting.
func staticAssetVersion(staticFS fs.FS) string {
	h := sha256.New()
	err := fs.WalkDir(staticFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, err := fs.ReadFile(staticFS, path)
		if err != nil {
			return err
		}
		_, _ = h.Write([]byte(path))
		_, _ = h.Write(raw)
		return nil
	})
	if err != nil {
		return fmt.Sprintf("%d", time.Now().Unix())
	}
	sum := h.Sum(nil)
	return fmt.Sprintf("%x", sum[:8])
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(s.staticFS))))

	mux.HandleFunc("GET /login", s.handleLoginGet)
	mux.HandleFunc("POST /login", s.handleLoginPost)
	mux.HandleFunc("GET /logout", s.handleLogout)

	// Poll-mode agent endpoints (no session cookie — copper_bmc dials these).
	mux.HandleFunc("POST /api/agent/poll/heartbeat", s.handleAgentPollHeartbeat)
	mux.HandleFunc("POST /api/agent/poll/commands/next", s.handleAgentPollNext)
	mux.HandleFunc("POST /api/agent/poll/commands/{id}/result", s.handleAgentPollResult)

	mux.Handle("GET /{$}", s.auth(s.handleIndex))
	mux.Handle("GET /servers", s.auth(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}))
	mux.Handle("GET /servers/new", s.auth(s.handleServerNewGet))
	mux.Handle("POST /servers/new", s.auth(s.handleServerNewPost))
	mux.Handle("GET /servers/import", s.auth(s.handleServerImportGet))
	mux.Handle("POST /servers/import", s.auth(s.handleServerImportPost))
	mux.Handle("POST /servers/sync-dhcp", s.auth(s.handleServersSyncDHCP))
	mux.Handle("GET /servers/{id}", s.auth(s.handleServerDetail))
	mux.Handle("GET /servers/{id}/edit", s.auth(s.handleServerEditGet))
	mux.Handle("POST /servers/{id}/edit", s.auth(s.handleServerEditPost))
	mux.Handle("POST /servers/{id}/delete", s.auth(s.handleServerDelete))

	mux.Handle("POST /servers/{id}/actions/probe", s.auth(s.handleActionProbe))
	mux.Handle("POST /servers/{id}/actions/inventory", s.auth(s.handleActionInventory))
	mux.Handle("POST /servers/{id}/actions/boot", s.auth(s.handleActionBoot))
	mux.Handle("POST /servers/{id}/actions/virtual-media", s.auth(s.handleActionVirtualMedia))
	mux.Handle("POST /servers/{id}/actions/reset", s.auth(s.handleActionReset))
	mux.Handle("POST /servers/{id}/actions/mount", s.auth(s.handleActionMount))
	mux.Handle("POST /servers/{id}/actions/convert-uefi", s.auth(s.handleActionConvertUEFI))
	mux.Handle("POST /servers/{id}/actions/lookup-mac", s.auth(s.handleLookupMAC))
	mux.Handle("POST /servers/{id}/actions/lookup-chassis", s.auth(s.handleLookupChassis))
	mux.Handle("POST /servers/{id}/mac", s.auth(s.handleSaveMAC))
	// Same payload as Inventory action; Content-Disposition for browser download + curl -OJ.
	mux.Handle("GET /servers/{id}/export/inventory.json", s.auth(s.handleExportInventory))

	// OOB / BMC-plane DHCP (policy pushed to copper_bmc).
	mux.Handle("GET /dhcp", s.auth(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dhcp/oob", http.StatusSeeOther)
	}))
	mux.Handle("GET /dhcp/oob", s.auth(s.handleDHCPGet))
	mux.Handle("POST /dhcp/oob", s.auth(s.handleDHCPSave))
	mux.Handle("POST /dhcp/oob/push", s.auth(s.handleDHCPPush))
	mux.Handle("POST /dhcp/oob/sync", s.auth(s.handleDHCPSync))
	mux.Handle("POST /dhcp", s.auth(s.handleDHCPSave))             // legacy form action
	mux.Handle("POST /dhcp/push", s.auth(s.handleDHCPPush))        // legacy

	// Data-plane DHCP (runs locally in copper_control_plane; PXE next-server / boot-file).
	mux.Handle("GET /dhcp/data", s.auth(s.handleDataDHCPGet))
	mux.Handle("POST /dhcp/data", s.auth(s.handleDataDHCPSave))
	mux.Handle("POST /dhcp/data/apply", s.auth(s.handleDataDHCPApply))

	mux.Handle("GET /activity", s.auth(s.handleActivityPage))
	mux.Handle("GET /jobs", s.auth(s.handleJobsPage))
	mux.Handle("GET /api/agent/activity", s.auth(s.handleActivityAPI))
	mux.Handle("GET /api/local/activity", s.auth(s.handleLocalActivityAPI))
	mux.Handle("GET /api/local/dhcp/leases", s.auth(s.handleLocalDHCPLeasesAPI))
	mux.Handle("POST /api/dhcp/data/claim-nics", s.auth(s.handleAPIClaimNICs))
	mux.Handle("POST /api/dhcp/data/assign-lease", s.auth(s.handleAPIAssignLease))
	mux.Handle("POST /api/dhcp/data/unassign-lease", s.auth(s.handleAPIUnassignLease))

	mux.Handle("GET /api/media/files", s.auth(s.handleAPIMediaFiles))
	mux.Handle("POST /api/servers/{id}/actions/inventory", s.auth(s.handleAPIInventory))
	mux.Handle("POST /api/servers/{id}/actions/reset", s.auth(s.handleAPIReset))
	mux.Handle("POST /api/servers/{id}/actions/mount", s.auth(s.handleAPIMount))
	mux.Handle("POST /api/servers/{id}/actions/convert-uefi", s.auth(s.handleAPIConvertUEFI))
	mux.Handle("POST /api/servers/{id}/actions/probe", s.auth(s.handleAPIProbe))
	mux.Handle("POST /api/servers/{id}/actions/detect-raid", s.auth(s.handleAPIDetectRAID))
	mux.Handle("GET /api/servers/{id}/storage/config", s.auth(s.handleAPIStorageConfig))
	mux.Handle("POST /api/servers/{id}/storage/exec", s.auth(s.handleAPIStorageExec))
	mux.Handle("POST /api/storage/exec", s.auth(s.handleAPIStorageExecBatch))
	mux.Handle("GET /api/servers/{id}/host/partitions", s.auth(s.handleAPIHostPartitions))
	mux.Handle("POST /api/servers/{id}/storage/wipe-partitions", s.auth(s.handleAPIStorageWipePartitions))
	mux.Handle("POST /api/storage/wipe-partitions", s.auth(s.handleAPIStorageWipePartitionsBatch))
	mux.Handle("GET /servers/{id}/storage", s.auth(s.handleStoragePage))
	mux.Handle("GET /storage", s.auth(s.handleStoragePage))
	mux.Handle("GET /servers/{id}/softraid", s.auth(s.handleSoftRAIDPage))
	mux.Handle("GET /softraid", s.auth(s.handleSoftRAIDPage))
	mux.Handle("GET /api/servers/{id}/softraid", s.auth(s.handleAPISoftRAIDGet))
	mux.Handle("POST /api/servers/{id}/softraid/scan", s.auth(s.handleAPISoftRAIDScan))
	mux.Handle("POST /api/servers/{id}/softraid/exec", s.auth(s.handleAPISoftRAIDExec))
	mux.Handle("POST /api/softraid/scan", s.auth(s.handleAPISoftRAIDScanBatch))
	mux.Handle("POST /api/softraid/exec", s.auth(s.handleAPISoftRAIDExecBatch))
	mux.Handle("GET /servers/{id}/deploy", s.auth(s.handleDeployPage))
	mux.Handle("GET /deploy", s.auth(s.handleDeployPage))
	mux.Handle("DELETE /api/servers/{id}", s.auth(s.handleAPIDelete))
	mux.Handle("POST /api/servers/{id}/delete", s.auth(s.handleAPIDelete)) // same as DELETE for simpler fetch
	mux.Handle("GET /api/servers/inventory.zip", s.auth(s.handleAPIInventoryZIP))

	mux.Handle("GET /api/data-media/files", s.auth(s.handleAPIDataMediaFiles))
	mux.Handle("GET /api/servers/{id}/layout", s.auth(s.handleAPILayoutGet))
	mux.Handle("POST /api/servers/{id}/layout/pin-direct", s.auth(s.handleAPILayoutPinDirect))
	mux.Handle("GET /api/servers/{id}/host/disks", s.auth(s.handleAPIHostDisks))
	mux.Handle("GET /api/servers/{id}/host/nics", s.auth(s.handleAPIHostNICs))
	mux.Handle("POST /api/servers/{id}/provision", s.auth(s.handleAPIProvisionStart))
	mux.Handle("POST /api/provision", s.auth(s.handleAPIProvisionStart))
	mux.Handle("GET /api/provision/jobs/{job_id}", s.auth(s.handleAPIProvisionJob))
	mux.Handle("GET /api/jobs/{job_id}", s.auth(s.handleAPIJobGet))
	mux.Handle("GET /api/jobs", s.auth(s.handleAPIJobsList))
	mux.Handle("GET /api/servers/busy", s.auth(s.handleAPIServersBusy))
	mux.Handle("POST /api/servers/labels", s.auth(s.handleAPIBatchLabels))

	mux.Handle("GET /settings", s.auth(s.handleSettingsGet))
	mux.Handle("POST /settings", s.auth(s.handleSettingsPost))
	mux.Handle("POST /settings/purge", s.auth(s.handleSettingsPurgePost))
	mux.Handle("GET /api/settings/batch", s.auth(s.handleAPIBatchSettings))

	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.isAuthed(r) {
			// API clients (fetch/XHR) must not follow an HTML login redirect —
			// that yields "<!DOCTYPE…" and breaks JSON.parse in the Jobs dock.
			if strings.HasPrefix(r.URL.Path, "/api/") {
				s.writeAPIJSON(w, http.StatusUnauthorized, map[string]any{
					"ok": false, "error": "unauthorized — log in again",
				})
				return
			}
			dest := "/login"
			if ret := safeNextPath(r.URL.RequestURI()); ret != "/" {
				dest = "/login?next=" + url.QueryEscape(ret)
			}
			http.Redirect(w, r, dest, http.StatusSeeOther)
			return
		}
		// Sliding session: keep the cookie alive while the admin is active
		// (long provisions / Jobs polling should not force a re-login).
		s.issueSessionCookie(w)
		next(w, r)
	})
}

func (s *Server) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	if s.isAuthed(r) {
		http.Redirect(w, r, safeNextPath(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	s.render(w, "login.html", pageData{
		Title: "Login",
		Next:  safeNextPath(r.URL.Query().Get("next")),
	})
}

func (s *Server) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	next := safeNextPath(r.FormValue("next"))
	if r.FormValue("password") != s.cfg.MasterPassword {
		s.render(w, "login.html", pageData{Title: "Login", Error: "Invalid master password", Next: next})
		return
	}
	s.issueSessionCookie(w)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "copper_session", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// Best-effort: fulfill declared CSV rows from live OOB DHCP leases.
	_ = s.reconcileOOBDHCP()

	servers, err := s.store.ListServers()
	if err != nil {
		s.render(w, "servers.html", pageData{Title: "Servers", Authed: true, Error: err.Error(), AgentURL: s.cfg.AgentBaseURL()})
		return
	}
	raidFamily := map[int64]string{}
	backfilled := false
	for i := range servers {
		before := servers[i].ChassisID
		s.backfillChassisIDFromInventory(&servers[i])
		if servers[i].ChassisID != before && servers[i].ChassisID != "" {
			backfilled = true
		}
		if d, err := s.loadDetect(servers[i].ID); err == nil && d.Family != "" {
			raidFamily[servers[i].ID] = string(d.Family)
		}
	}
	if backfilled {
		if refreshed, err := s.store.ListServers(); err == nil {
			servers = refreshed
		}
	}
	filter := parseServerListFilter(r.URL.Query())
	filtered := applyServerListFilter(servers, filter)
	allLabels, _ := s.store.ListLabels()
	labelSet := map[string]bool{}
	for _, name := range allLabels {
		for _, fl := range filter.Labels {
			if strings.EqualFold(name, fl) {
				labelSet[name] = true
				break
			}
		}
	}
	data := pageData{
		Title:          "Servers",
		Authed:         true,
		AgentURL:       s.cfg.AgentBaseURL(),
		Servers:        filtered,
		RaidFamily:     raidFamily,
		DataIPs:        s.dataIPsByServerID(),
		AllLabels:      allLabels,
		FilterLabels:   filter.Labels,
		FilterLabelSet: labelSet,
		FilterStatus:   filter.Status,
		FilterQ:        filter.Q,
		FilterActive:   filter.Active(),
	}
	if flash := r.URL.Query().Get("flash"); flash != "" {
		data.Flash = flash
	}
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		data.Error = errMsg
	}
	s.render(w, "servers.html", data)
}

func (s *Server) handleServersSyncDHCP(w http.ResponseWriter, r *http.Request) {
	res := s.reconcileOOBDHCP()
	if res.Error != "" {
		http.Redirect(w, r, "/?error="+urlQuery(res.Error), http.StatusSeeOther)
		return
	}
	msg := formatSyncFlash(res)
	http.Redirect(w, r, "/?flash="+urlQuery(msg), http.StatusSeeOther)
}

const csvSample = `chassis_id,bmc_ip,mac_address,username,password,provisioned_hostname,provisioned_ip_cidr,provisioned_gw,provisioned_dns,labels
rack1-node3,,,admin,secret,node-03,10.0.0.13/24,10.0.0.1,"1.1.1.1, 8.8.8.8","prod;rack-1"
,192.168.10.50,,root,changeme,,,,,gpu
,,aa:bb:cc:dd:ee:01,ilo,password,,,,,
`

func (s *Server) handleServerImportGet(w http.ResponseWriter, _ *http.Request) {
	s.render(w, "server_import.html", pageData{
		Title:     "Import servers",
		Authed:    true,
		CSVSample: csvSample,
	})
}

func (s *Server) handleServerImportPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		s.render(w, "server_import.html", pageData{
			Title: "Import servers", Authed: true, CSVSample: csvSample,
			Error: "invalid upload: " + err.Error(),
		})
		return
	}
	file, _, err := r.FormFile("csv")
	if err != nil {
		s.render(w, "server_import.html", pageData{
			Title: "Import servers", Authed: true, CSVSample: csvSample,
			Error: "choose a CSV file",
		})
		return
	}
	defer file.Close()

	rows, parseRes := parseServersCSV(file)
	if parseRes.ParseErr != "" {
		s.render(w, "server_import.html", pageData{
			Title: "Import servers", Authed: true, CSVSample: csvSample,
			Error: parseRes.ParseErr, ImportResult: &parseRes,
		})
		return
	}
	applyRes := s.applyCSVRows(rows)
	applyRes.RowErrs = append(parseRes.RowErrs, applyRes.RowErrs...)

	// Immediately try to bind against current OOB leases.
	bind := s.reconcileOOBDHCP()
	flash := fmt.Sprintf("Import: %d created, %d updated, %d row error(s).",
		applyRes.Created, applyRes.Updated, len(applyRes.RowErrs))
	if bind.Error == "" {
		flash += fmt.Sprintf(" DHCP bind: %d matched (%d updated).", bind.Bound, bind.Updated)
	} else {
		flash += " DHCP bind skipped: " + bind.Error
	}

	if applyRes.Created+applyRes.Updated == 0 && len(applyRes.RowErrs) > 0 {
		s.render(w, "server_import.html", pageData{
			Title: "Import servers", Authed: true, CSVSample: csvSample,
			Error: "no rows imported", ImportResult: &applyRes,
		})
		return
	}
	s.render(w, "server_import.html", pageData{
		Title: "Import servers", Authed: true, CSVSample: csvSample,
		Flash: flash, ImportResult: &applyRes,
	})
}

func (s *Server) handleServerNewGet(w http.ResponseWriter, _ *http.Request) {
	allLabels, _ := s.store.ListLabels()
	s.render(w, "server_form.html", pageData{Title: "Add server", Authed: true, AllLabels: allLabels})
}

func (s *Server) handleServerNewPost(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	srv := store.Server{
		Name:      strings.TrimSpace(r.FormValue("name")),
		Endpoint:  strings.TrimSpace(r.FormValue("endpoint")),
		Username:  strings.TrimSpace(r.FormValue("username")),
		Password:  r.FormValue("password"),
		Insecure:  r.FormValue("insecure") == "1",
		ChassisID: strings.TrimSpace(r.FormValue("chassis_id")),
		Notes:     strings.TrimSpace(r.FormValue("notes")),
		Source:    store.SourceManual,
		Status:    store.StatusDeclared, // Probe (or OOB DHCP bind) required before ops
	}
	allLabels, _ := s.store.ListLabels()
	formData := func(errMsg string) pageData {
		return pageData{Title: "Add server", Authed: true, Error: errMsg, Server: &srv, AllLabels: allLabels}
	}
	labels, err := parseLabelsForm(r)
	if err != nil {
		s.render(w, "server_form.html", formData(err.Error()))
		return
	}
	srv.Labels = labels
	mac, err := normalizeMACInput(r.FormValue("mac"))
	if err != nil {
		srv.MAC = strings.TrimSpace(r.FormValue("mac"))
		s.render(w, "server_form.html", formData(err.Error()))
		return
	}
	srv.MAC = mac
	prov, err := parseProvisionedForm(
		r.FormValue("provisioned_hostname"),
		r.FormValue("provisioned_ip_cidr"),
		r.FormValue("provisioned_gw"),
		r.FormValue("provisioned_dns"),
	)
	srv.ProvisionedHostname = prov.Hostname
	srv.ProvisionedIPCIDR = prov.IPCIDR
	srv.ProvisionedGW = prov.GW
	srv.ProvisionedDNS = prov.DNS
	if err != nil {
		s.render(w, "server_form.html", formData(err.Error()))
		return
	}
	if srv.Name == "" || srv.Username == "" || srv.Password == "" {
		s.render(w, "server_form.html", formData("Name, username and password are required"))
		return
	}
	if !declaredIdentityPresent(srv.ChassisID, srv.Endpoint, srv.MAC) {
		s.render(w, "server_form.html", formData(errDeclaredIdentity))
		return
	}
	id, err := s.store.CreateServer(srv)
	if err != nil {
		s.render(w, "server_form.html", formData(err.Error()))
		return
	}
	if err := s.store.SetServerLabels(id, labels); err != nil {
		s.render(w, "server_form.html", formData(err.Error()))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/servers/%d", id), http.StatusSeeOther)
}

func (s *Server) handleServerDetail(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.backfillChassisIDFromInventory(srv)
	data := pageData{Title: srv.Name, Authed: true, Server: srv}
	if flash := r.URL.Query().Get("flash"); flash != "" {
		data.Flash = flash
	}
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		data.Error = errMsg
	}
	if ip, _, err := s.dataIPForServer(srv); err == nil {
		data.DataIP = ip
	}
	if snap, err := s.store.GetSnapshot(srv.ID, "inventory"); err == nil && snap != nil {
		data.Result = snap.Payload
		data.ResultKind = "inventory"
		data.ResultFetchedAt = snap.FetchedAt.Local().Format("2006-01-02 15:04:05 MST")
	}
	s.render(w, "server_detail.html", s.withMediaCatalog(data))
}

func (s *Server) handleSaveMAC(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	_ = r.ParseForm()
	mac, err := normalizeMACInput(r.FormValue("mac"))
	if err != nil {
		http.Redirect(w, r, fmt.Sprintf("/servers/%d?error=%s", srv.ID, urlQuery(err.Error())), http.StatusSeeOther)
		return
	}
	srv.MAC = mac
	if err := s.store.UpdateServer(*srv); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/servers/%d?error=%s", srv.ID, urlQuery(err.Error())), http.StatusSeeOther)
		return
	}
	msg := "iLO MAC cleared"
	if mac != "" {
		msg = "iLO MAC saved: " + mac
	}
	http.Redirect(w, r, fmt.Sprintf("/servers/%d?flash=%s", srv.ID, urlQuery(msg)), http.StatusSeeOther)
}

func (s *Server) handleServerEditGet(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.backfillChassisIDFromInventory(srv)
	allLabels, _ := s.store.ListLabels()
	data := pageData{Title: "Edit server", Authed: true, Editing: true, Server: srv, AllLabels: allLabels}
	if flash := r.URL.Query().Get("flash"); flash != "" {
		data.Flash = flash
	}
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		data.Error = errMsg
	}
	s.render(w, "server_form.html", data)
}

func (s *Server) handleLookupMAC(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	dest := fmt.Sprintf("/servers/%d", srv.ID)
	ip, err := hostFromEndpoint(srv.Endpoint)
	if err != nil {
		http.Redirect(w, r, dest+"?error="+urlQuery(err.Error()), http.StatusSeeOther)
		return
	}
	mac, err := s.agent.LookupARP(ip)
	if err != nil {
		http.Redirect(w, r, dest+"?error="+urlQuery(err.Error()), http.StatusSeeOther)
		return
	}
	mac, err = normalizeMACInput(mac)
	if err != nil {
		http.Redirect(w, r, dest+"?error="+urlQuery(err.Error()), http.StatusSeeOther)
		return
	}
	srv.MAC = mac
	if err := s.store.UpdateServer(*srv); err != nil {
		http.Redirect(w, r, dest+"?error="+urlQuery(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, dest+"?flash="+urlQuery("iLO MAC set to "+mac+" (from "+ip+")"), http.StatusSeeOther)
}

func (s *Server) handleLookupChassis(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	dest := fmt.Sprintf("/servers/%d", srv.ID)

	mac := strings.TrimSpace(srv.MAC)
	ip := ""
	if host, err := hostFromEndpoint(srv.Endpoint); err == nil {
		ip = host
	}
	if mac == "" && ip == "" && strings.TrimSpace(srv.ChassisID) == "" {
		http.Redirect(w, r, dest+"?error="+urlQuery("need MAC, endpoint IP, or existing chassis id for LLDP lookup"), http.StatusSeeOther)
		return
	}

	n, err := s.agent.LookupLLDP(mac, ip, srv.ChassisID)
	if err != nil && mac == "" && ip != "" {
		// Fall back: ARP the IP, then LLDP by MAC.
		if resolved, aerr := s.agent.LookupARP(ip); aerr == nil {
			if hw, nerr := normalizeMACInput(resolved); nerr == nil && hw != "" {
				mac = hw
				n, err = s.agent.LookupLLDP(mac, "", "")
			}
		}
	}
	if err != nil {
		http.Redirect(w, r, dest+"?error="+urlQuery(err.Error()), http.StatusSeeOther)
		return
	}

	changed, err := s.store.EnrichFromLLDP(srv, n.ChassisID, n.SrcMAC, n.MgmtAddrs)
	if err != nil {
		http.Redirect(w, r, dest+"?error="+urlQuery(err.Error()), http.StatusSeeOther)
		return
	}
	msg := "LLDP: no fields to update (already set)"
	if changed {
		msg = "LLDP chassis set to " + n.ChassisID
		if n.SrcMAC != "" {
			msg += " (src MAC " + n.SrcMAC + ")"
		}
	}
	http.Redirect(w, r, dest+"?flash="+urlQuery(msg), http.StatusSeeOther)
}

func hostFromEndpoint(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", fmt.Errorf("endpoint is empty")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid endpoint: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("endpoint has no host")
	}
	if net.ParseIP(host) == nil {
		// Hostname: still try as-is; ARP needs an IP. Resolve A record.
		ips, err := net.LookupIP(host)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", host, err)
		}
		for _, ip := range ips {
			if v4 := ip.To4(); v4 != nil {
				return v4.String(), nil
			}
		}
		return "", fmt.Errorf("no IPv4 for %s", host)
	}
	return host, nil
}

func (s *Server) handleServerEditPost(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	_ = r.ParseForm()
	allLabels, _ := s.store.ListLabels()
	formData := func(errMsg string) pageData {
		return pageData{Title: "Edit server", Authed: true, Editing: true, Server: srv, Error: errMsg, AllLabels: allLabels}
	}
	srv.Name = strings.TrimSpace(r.FormValue("name"))
	srv.Endpoint = strings.TrimSpace(r.FormValue("endpoint"))
	srv.Username = strings.TrimSpace(r.FormValue("username"))
	if pw := r.FormValue("password"); pw != "" {
		srv.Password = pw
	}
	srv.Insecure = r.FormValue("insecure") == "1"
	srv.ChassisID = strings.TrimSpace(r.FormValue("chassis_id"))
	srv.Notes = strings.TrimSpace(r.FormValue("notes"))
	labels, err := parseLabelsForm(r)
	if err != nil {
		s.render(w, "server_form.html", formData(err.Error()))
		return
	}
	srv.Labels = labels
	mac, err := normalizeMACInput(r.FormValue("mac"))
	if err != nil {
		srv.MAC = strings.TrimSpace(r.FormValue("mac"))
		s.render(w, "server_form.html", formData(err.Error()))
		return
	}
	srv.MAC = mac
	prov, err := parseProvisionedForm(
		r.FormValue("provisioned_hostname"),
		r.FormValue("provisioned_ip_cidr"),
		r.FormValue("provisioned_gw"),
		r.FormValue("provisioned_dns"),
	)
	srv.ProvisionedHostname = prov.Hostname
	srv.ProvisionedIPCIDR = prov.IPCIDR
	srv.ProvisionedGW = prov.GW
	srv.ProvisionedDNS = prov.DNS
	if err != nil {
		s.render(w, "server_form.html", formData(err.Error()))
		return
	}
	if strings.TrimSpace(srv.Name) == "" || strings.TrimSpace(srv.Username) == "" {
		s.render(w, "server_form.html", formData("Name and username are required"))
		return
	}
	if !declaredIdentityPresent(srv.ChassisID, srv.Endpoint, srv.MAC) {
		s.render(w, "server_form.html", formData(errDeclaredIdentity))
		return
	}
	if err := s.store.UpdateServer(*srv); err != nil {
		s.render(w, "server_form.html", formData(err.Error()))
		return
	}
	if err := s.store.SetServerLabels(srv.ID, labels); err != nil {
		s.render(w, "server_form.html", formData(err.Error()))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/servers/%d", srv.ID), http.StatusSeeOther)
}

// Same identity rule as CSV import: username/password (and a name, on the form)
// are required, and at least one of chassis ID, BMC endpoint, or BMC MAC.
const errDeclaredIdentity = "Need at least one of chassis ID, Redfish endpoint, or iLO / BMC MAC"

func declaredIdentityPresent(chassisID, endpoint, mac string) bool {
	return strings.TrimSpace(chassisID) != "" ||
		strings.TrimSpace(endpoint) != "" ||
		strings.TrimSpace(mac) != ""
}

func (s *Server) handleServerDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = s.store.DeleteServer(id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleActionProbe(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	dest := fmt.Sprintf("/servers/%d", srv.ID)
	if strings.TrimSpace(srv.Endpoint) == "" {
		http.Redirect(w, r, dest+"?error="+urlQuery("set a BMC IP / Redfish endpoint first (Edit), then Probe"), http.StatusSeeOther)
		return
	}
	var raw json.RawMessage
	err = s.withServerLock(r.Context(), srv.ID, func(ctx context.Context) error {
		var perr error
		raw, perr = s.agent.Probe(srv)
		return perr
	})
	if err != nil {
		http.Redirect(w, r, dest+"?error="+urlQuery("probe failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	srv.Status = store.StatusBound
	if err := s.store.UpdateServer(*srv); err != nil {
		http.Redirect(w, r, dest+"?error="+urlQuery(err.Error()), http.StatusSeeOther)
		return
	}
	// Show a short probe payload for confirmation.
	formatted := string(raw)
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err == nil {
		if pretty, err := json.MarshalIndent(parsed, "", "  "); err == nil {
			formatted = string(pretty)
		}
	}
	s.render(w, "server_detail.html", s.withMediaCatalog(pageData{
		Title: srv.Name, Authed: true, Server: srv,
		Flash: "Probe OK — credentials work; server is now bound.",
		Result: formatted, ResultKind: "probe",
	}))
}

func (s *Server) handleActionInventory(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, "inventory", true, func(srv *store.Server) (json.RawMessage, error) {
		return s.agent.Inventory(srv)
	})
}
func (s *Server) handleActionBoot(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, "boot", false, func(srv *store.Server) (json.RawMessage, error) { return s.agent.Boot(srv) })
}
func (s *Server) handleActionVirtualMedia(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, "virtual-media", false, func(srv *store.Server) (json.RawMessage, error) {
		return s.agent.VirtualMedia(srv)
	})
}
func (s *Server) handleActionReset(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	resetType := normalizeResetType(r.FormValue("reset_type"))
	if resetType == "" {
		resetType = "ForceRestart"
	}
	s.runAction(w, r, "reset", false, func(srv *store.Server) (json.RawMessage, error) {
		return s.agent.Reset(srv, map[string]string{"reset_type": resetType})
	})
}
func (s *Server) handleActionMount(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	imageURL := strings.TrimSpace(r.FormValue("image_url"))
	if imageURL == "" {
		srv, err := s.serverFromPath(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		s.render(w, "server_detail.html", s.withMediaCatalog(pageData{
			Title: srv.Name, Authed: true, Server: srv,
			Error: "image_url is required — pick a media file or enter a URL",
		}))
		return
	}
	body := map[string]any{
		"image_url": imageURL,
		"boot_once": r.FormValue("boot_once") == "1",
		"reboot":    r.FormValue("reboot") == "1",
	}
	s.runAction(w, r, "mount", false, func(srv *store.Server) (json.RawMessage, error) {
		return s.agent.MountMedia(srv, body)
	})
}

func (s *Server) handleActionConvertUEFI(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	reboot := r.FormValue("reboot") != "0"
	s.runAction(w, r, "convert-uefi", false, func(srv *store.Server) (json.RawMessage, error) {
		return s.agent.ConvertToUEFI(srv, map[string]any{"reboot": reboot})
	})
}

// handleExportInventory serves the last cached inventory from SQLite (no Redfish call).
// Run Inventory first to refresh the cache. Suitable for serial curl downloads.
func (s *Server) handleExportInventory(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	snap, err := s.store.GetSnapshot(srv.ID, "inventory")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if snap == nil {
		http.Error(w, "no cached inventory; click Inventory first", http.StatusNotFound)
		return
	}
	name := sanitizeFilename(srv.Name)
	if name == "" {
		name = fmt.Sprintf("server-%d", srv.ID)
	}
	filename := name + "-inventory.json"
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Copper-Fetched-At", snap.FetchedAt.UTC().Format(time.RFC3339))
	_, _ = w.Write([]byte(snap.Payload))
	if !strings.HasSuffix(snap.Payload, "\n") {
		_, _ = w.Write([]byte("\n"))
	}
}

func (s *Server) runAction(w http.ResponseWriter, r *http.Request, kind string, persist bool, fn func(*store.Server) (json.RawMessage, error)) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if srv.Status == store.StatusDeclared {
		s.render(w, "server_detail.html", s.withMediaCatalog(pageData{
			Title: srv.Name, Authed: true, Server: srv,
			Error: "server is still declared — Probe with credentials (or wait for OOB DHCP bind) before Redfish actions",
		}))
		return
	}
	if strings.TrimSpace(srv.Endpoint) == "" {
		s.render(w, "server_detail.html", s.withMediaCatalog(pageData{
			Title: srv.Name, Authed: true, Server: srv,
			Error: "no BMC endpoint yet — set an IP and Probe, or wait for OOB DHCP bind",
		}))
		return
	}
	var raw json.RawMessage
	err = s.withServerLock(r.Context(), srv.ID, func(ctx context.Context) error {
		var aerr error
		raw, aerr = fn(srv)
		return aerr
	})
	if err != nil {
		s.render(w, "server_detail.html", s.withMediaCatalog(pageData{
			Title: srv.Name, Authed: true, Server: srv, Error: err.Error(),
		}))
		return
	}

	formatted := string(raw)
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err == nil {
		if pretty, err := json.MarshalIndent(parsed, "", "  "); err == nil {
			formatted = string(pretty)
		}
	}

	fetchedAt := ""
	if persist {
		if err := s.store.SaveSnapshot(srv.ID, kind, formatted); err != nil {
			s.render(w, "server_detail.html", s.withMediaCatalog(pageData{
				Title: srv.Name, Authed: true, Server: srv,
				Error: "inventory ok but failed to cache: " + err.Error(),
				Result: formatted, ResultKind: kind,
			}))
			return
		}
		fetchedAt = time.Now().Local().Format("2006-01-02 15:04:05 MST")
		if kind == "inventory" {
			if _, err := s.applyChassisIDFromInventory(srv, formatted); err != nil {
				s.render(w, "server_detail.html", s.withMediaCatalog(pageData{
					Title: srv.Name, Authed: true, Server: srv,
					Error: "inventory cached but chassis_id update failed: " + err.Error(),
					Result: formatted, ResultKind: kind, ResultFetchedAt: fetchedAt,
				}))
				return
			}
		}
	}

	s.render(w, "server_detail.html", s.withMediaCatalog(pageData{
		Title: srv.Name, Authed: true, Server: srv,
		Result: formatted, ResultKind: kind, ResultFetchedAt: fetchedAt,
	}))
}

// withMediaCatalog loads copper_bmc's OOB media folder listing for the mount UI.
func (s *Server) withMediaCatalog(data pageData) pageData {
	cat, err := s.agent.MediaFiles()
	if err != nil {
		data.MediaError = err.Error()
		return data
	}
	data.MediaBaseURL = strings.TrimRight(cat.BaseURL, "/")
	data.MediaFolder = cat.Folder
	data.MediaNote = cat.Note
	data.MediaFiles = make([]mediaFileRow, 0, len(cat.Files))
	for _, f := range cat.Files {
		row := mediaFileRow{
			Path:      f.Path,
			SizeHuman: formatByteSize(f.Size),
		}
		if data.MediaBaseURL != "" {
			row.URL = data.MediaBaseURL + "/" + strings.TrimLeft(f.Path, "/")
		}
		data.MediaFiles = append(data.MediaFiles, row)
	}
	return data
}

func formatByteSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	v := float64(n)
	for _, u := range units {
		v /= 1024
		if v < 1024 {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f PiB", v/1024)
}

func sanitizeFilename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-._")
}

func (s *Server) handleDHCPGet(w http.ResponseWriter, r *http.Request) {
	data := s.dhcpPageData("")
	if flash := r.URL.Query().Get("flash"); flash != "" {
		data.Flash = flash
	}
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		data.Error = errMsg
	}
	s.render(w, "dhcp.html", data)
}

func (s *Server) handleActivityPage(w http.ResponseWriter, _ *http.Request) {
	s.render(w, "activity.html", pageData{
		Title:    "Agent activity",
		Authed:   true,
		AgentURL: s.cfg.AgentBaseURL(),
	})
}

func (s *Server) handleActivityAPI(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var since uint64
	if v := strings.TrimSpace(q.Get("since")); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			http.Error(w, "invalid since", http.StatusBadRequest)
			return
		}
		since = n
	}
	limit := 200
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = n
	}
	component := strings.TrimSpace(q.Get("component"))

	out, err := s.agent.Activity(since, limit, component)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) handleLocalActivityAPI(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var since uint64
	if v := strings.TrimSpace(q.Get("since")); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			http.Error(w, "invalid since", http.StatusBadRequest)
			return
		}
		since = n
	}
	limit := 200
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = n
	}
	component := strings.TrimSpace(q.Get("component"))

	events := s.localAct.Since(since, limit, component)
	var nextSince uint64 = since
	for _, ev := range events {
		if ev.ID > nextSince {
			nextSince = ev.ID
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"events":     events,
		"next_since": nextSince,
	})
}

// handleLocalDHCPLeasesAPI returns every active Data DHCP lease from this process.
// Inventory is optional enrichment only — leases appear even without Probe/Inventory.
func (s *Server) handleLocalDHCPLeasesAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	running := false
	var rows []leaseRow
	if s.localDHCP != nil {
		st := s.localDHCP.Status()
		running = st.Running
		leases := s.localDHCP.Leases()
		rows = leaseRowsFromDHCP(leases)
		data := pageData{Leases: rows}
		s.enrichDataLeasesWith(s.listInventoryHostNICs(), &data)
		rows = data.Leases
		sortLeaseRows(rows)
	}
	if rows == nil {
		rows = []leaseRow{}
	}
	watchSec := store.DefaultAgentWatchSeconds
	if rt, err := s.store.GetBatchRuntime(); err == nil {
		watchSec = rt.Normalize().AgentWatchSeconds
	}
	servers := []map[string]any{}
	if list, err := s.store.ListServers(); err == nil {
		for _, srv := range list {
			servers = append(servers, map[string]any{"id": srv.ID, "name": srv.Name})
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"running":             running,
		"leases":              rows,
		"servers":             servers, // for manual Assign dropdown
		"agent_watch_seconds": watchSec,
	})
}

func (s *Server) handleDHCPSave(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	cfg := dhcpFromForm(r, false)
	cfg.NextServer = ""
	cfg.BootFile = ""
	if err := s.store.SaveAgentDHCPConfig(cfg); err != nil {
		data := s.dhcpPageData("")
		data.Error = err.Error()
		data.DHCP = cfg
		s.fillDHCPFormTexts(&data)
		s.render(w, "dhcp.html", data)
		return
	}
	if err := s.pushDHCP(cfg); err != nil {
		http.Redirect(w, r, "/dhcp/oob?error="+urlQuery(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dhcp/oob?flash="+urlQuery("Saved and pushed to agent"), http.StatusSeeOther)
}

func (s *Server) handleDHCPPush(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetAgentDHCPConfig()
	if err != nil {
		http.Redirect(w, r, "/dhcp/oob?error="+urlQuery(err.Error()), http.StatusSeeOther)
		return
	}
	if err := s.pushDHCP(cfg); err != nil {
		http.Redirect(w, r, "/dhcp/oob?error="+urlQuery(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dhcp/oob?flash="+urlQuery("Pushed to agent"), http.StatusSeeOther)
}

func (s *Server) handleDHCPSync(w http.ResponseWriter, r *http.Request) {
	res := s.reconcileOOBDHCP()
	if res.Error != "" {
		http.Redirect(w, r, "/dhcp/oob?error="+urlQuery(res.Error), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dhcp/oob?flash="+urlQuery(formatSyncFlash(res)), http.StatusSeeOther)
}

func formatSyncFlash(res BindResult) string {
	msg := fmt.Sprintf("Synced from agent: %d lease(s), %d matched, %d updated, %d unmatched; LLDP: %d neighbor(s), %d enriched",
		res.LeasesSeen, res.Bound, res.Updated, res.Unmatched, res.LLDPSeen, res.LLDPEnriched)
	if res.LLDPWarning != "" {
		msg += " — LLDP skipped: " + res.LLDPWarning
	}
	return msg
}

func (s *Server) handleDataDHCPGet(w http.ResponseWriter, r *http.Request) {
	data := s.dataDHCPPageData("")
	if flash := r.URL.Query().Get("flash"); flash != "" {
		data.Flash = flash
	}
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		data.Error = errMsg
	}
	s.render(w, "dhcp_data.html", data)
}

func (s *Server) handleDataDHCPSave(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	cfg := dhcpFromForm(r, true)
	if err := s.store.SaveLocalDHCPConfig(cfg); err != nil {
		data := s.dataDHCPPageData(err.Error())
		data.DHCP = cfg
		// Rebuild pickers against the submitted (unsaved) config.
		leaseByMAC := map[string]string{}
		for _, l := range data.Leases {
			mac, e := normalizeMACInput(l.MAC)
			if e != nil || mac == "" || l.IP == "" {
				continue
			}
			leaseByMAC[strings.ToLower(mac)] = l.IP
		}
		s.fillDataDHCPFormTexts(&data, s.listInventoryHostNICs(), leaseByMAC)
		s.render(w, "dhcp_data.html", data)
		return
	}
	if err := s.applyLocalDHCP(cfg); err != nil {
		http.Redirect(w, r, "/dhcp/data?error="+urlQuery(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dhcp/data?flash="+urlQuery("Saved and applied on this host"), http.StatusSeeOther)
}

func (s *Server) handleDataDHCPApply(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetLocalDHCPConfig()
	if err != nil {
		http.Redirect(w, r, "/dhcp/data?error="+urlQuery(err.Error()), http.StatusSeeOther)
		return
	}
	if err := s.applyLocalDHCP(cfg); err != nil {
		http.Redirect(w, r, "/dhcp/data?error="+urlQuery(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dhcp/data?flash="+urlQuery("Applied on this host"), http.StatusSeeOther)
}

func (s *Server) applyLocalDHCP(cfg copper_dhcp.Config) error {
	if s.localDHCP == nil {
		return fmt.Errorf("local DHCP manager is not initialized")
	}
	if s.dataMedia != nil && cfg.Interface != "" {
		s.dataMedia.SetIfaceHint(cfg.Interface)
	}
	return s.localDHCP.Apply(cfg)
}

func (s *Server) dataDHCPPageData(errMsg string) pageData {
	cfg, _ := s.store.GetLocalDHCPConfig()
	if cfg.Listen == "" {
		cfg.Listen = "0.0.0.0:67"
	}
	if cfg.LeaseTime == "" {
		cfg.LeaseTime = "1h"
	}
	data := pageData{
		Title:  "Data DHCP",
		Authed: true,
		Error:  errMsg,
		DHCP:   cfg,
	}
	if rt, err := s.store.GetBatchRuntime(); err == nil {
		data.BatchRuntime = rt.Normalize()
	} else {
		data.BatchRuntime = store.DefaultBatchRuntime()
	}

	leaseByMAC := map[string]string{}
	if s.localDHCP != nil {
		st := s.localDHCP.Status()
		data.DHCPRunning = st.Running
		leases := s.localDHCP.Leases()
		data.Leases = leaseRowsFromDHCP(leases)
		for _, l := range leases {
			mac, err := normalizeMACInput(l.MAC)
			if err != nil || mac == "" || l.IP == nil {
				continue
			}
			leaseByMAC[strings.ToLower(mac)] = l.IP.String()
		}
		if b, err := json.MarshalIndent(map[string]any{"leases": leases}, "", "  "); err == nil {
			data.LeasesJSON = string(b)
		}
	}

	hostNICs := s.listInventoryHostNICs()
	s.enrichDataLeasesWith(hostNICs, &data)
	sortLeaseRows(data.Leases)
	s.fillDataDHCPFormTexts(&data, hostNICs, leaseByMAC)
	return data
}

func (s *Server) pushDHCP(cfg copper_dhcp.Config) error {
	role, err := s.agent.DHCPRoleEnabled()
	if err != nil {
		return fmt.Errorf("agent health: %w", err)
	}
	if !role {
		return fmt.Errorf("agent DHCP role is disabled — restart copper_bmc and enable the DHCP role")
	}
	// OOB agent DHCP is addressing-only; never push PXE options even if
	// an older saved config still has them.
	cfg.NextServer = ""
	cfg.BootFile = ""
	_, err = s.agent.PutDHCPConfig(cfg)
	return err
}

func (s *Server) dhcpPageData(errMsg string) pageData {
	cfg, _ := s.store.GetAgentDHCPConfig()
	if cfg.Listen == "" {
		cfg.Listen = "0.0.0.0:67"
	}
	if cfg.LeaseTime == "" {
		cfg.LeaseTime = "1h"
	}
	data := pageData{
		Title:          "OOB DHCP",
		Authed:         true,
		Error:          errMsg,
		DHCP:           cfg,
		AgentWatchNote: s.agentWatchNote(),
	}
	s.fillDHCPFormTexts(&data)

	role, err := s.agent.DHCPRoleEnabled()
	if err != nil {
		data.AgentError = err.Error()
	} else {
		data.DHCPRole = role
		if role {
			if st, err := s.agent.GetDHCPConfig(); err == nil {
				if running, ok := st["running"].(bool); ok {
					data.DHCPRunning = running
				}
			}
			if leases, err := s.agent.DHCPLeases(); err == nil {
				data.Leases = leaseRowsFromAgent(leases)
				if b, err := json.MarshalIndent(leases, "", "  "); err == nil {
					data.LeasesJSON = string(b)
				}
			}
		}
	}
	return data
}

func leaseRowsFromDHCP(leases []copper_dhcp.Lease) []leaseRow {
	out := make([]leaseRow, 0, len(leases))
	for _, l := range leases {
		ip := ""
		if l.IP != nil {
			ip = l.IP.String()
		}
		out = append(out, leaseRow{
			MAC:       l.MAC,
			IP:        ip,
			ExpiresAt: l.ExpiresAt.Local().Format("2006-01-02 15:04:05"),
			Reserved:  l.Reserved,
		})
	}
	sortLeaseRows(out)
	return out
}

// sortLeaseRows keeps the leases table order stable across refreshes (by MAC, then IP).
func sortLeaseRows(rows []leaseRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		mi := strings.ToLower(strings.TrimSpace(rows[i].MAC))
		mj := strings.ToLower(strings.TrimSpace(rows[j].MAC))
		if mi != mj {
			return mi < mj
		}
		return rows[i].IP < rows[j].IP
	})
}

func leaseRowsFromAgent(raw map[string]any) []leaseRow {
	views, err := parseLeasesPayload(raw)
	if err != nil {
		return nil
	}
	// Prefer typed decode for expiry / reserved when present.
	b, err := json.Marshal(raw)
	if err == nil {
		var typed struct {
			Leases []copper_dhcp.Lease `json:"leases"`
		}
		if json.Unmarshal(b, &typed) == nil && len(typed.Leases) > 0 {
			return leaseRowsFromDHCP(typed.Leases)
		}
	}
	out := make([]leaseRow, 0, len(views))
	for _, v := range views {
		out = append(out, leaseRow{MAC: v.MAC, IP: v.IP})
	}
	sortLeaseRows(out)
	return out
}

func (s *Server) fillDHCPFormTexts(data *pageData) {
	s.fillDHCPCommonTexts(data)

	selected := map[string]struct{}{}
	for _, m := range data.DHCP.AllowMACs {
		if k, err := normalizeMACInput(m); err == nil && k != "" {
			selected[strings.ToLower(k)] = struct{}{}
		}
	}

	servers, _ := s.store.ListServers()
	knownKeys := map[string]struct{}{}
	data.KnownMACs = nil
	for _, srv := range servers {
		mac, err := normalizeMACInput(srv.MAC)
		if err != nil || mac == "" {
			continue
		}
		key := strings.ToLower(mac)
		knownKeys[key] = struct{}{}
		_, ok := selected[key]
		data.KnownMACs = append(data.KnownMACs, knownMAC{
			Name:     srv.Name,
			MAC:      mac,
			Selected: ok,
		})
	}

	var extra []string
	for _, m := range data.DHCP.AllowMACs {
		mac, err := normalizeMACInput(m)
		if err != nil || mac == "" {
			continue
		}
		if _, known := knownKeys[strings.ToLower(mac)]; !known {
			extra = append(extra, mac)
		}
	}
	data.AllowMACsText = strings.Join(extra, "\n")
}

// fillDataDHCPFormTexts builds accordion groups from host NIC MACs in cached
// Redfish inventory, binding live lease IPs so admins can find who got what.
// Live leases whose MACs are not in inventory are appended as their own group
// so every handed-out IP is visible without Probe/Inventory.
func (s *Server) fillDataDHCPFormTexts(data *pageData, hostNICs []inventoryNIC, leaseByMAC map[string]string) {
	s.fillDHCPCommonTexts(data)
	if leaseByMAC == nil {
		leaseByMAC = map[string]string{}
	}

	selected := map[string]struct{}{}
	for _, m := range data.DHCP.AllowMACs {
		if k, err := normalizeMACInput(m); err == nil && k != "" {
			selected[strings.ToLower(k)] = struct{}{}
		}
	}
	reserveByMAC := map[string]string{}
	for _, r := range data.DHCP.Reservations {
		mac, err := normalizeMACInput(r.MAC)
		if err != nil || mac == "" {
			continue
		}
		reserveByMAC[strings.ToLower(mac)] = strings.TrimSpace(r.IP)
	}

	order := make([]int64, 0)
	byID := map[int64]*hostServerGroup{}
	knownKeys := map[string]struct{}{}

	for _, n := range hostNICs {
		key := strings.ToLower(n.MAC)
		knownKeys[key] = struct{}{}
		g, ok := byID[n.ServerID]
		if !ok {
			g = &hostServerGroup{
				ID:       n.ServerID,
				Name:     n.ServerName,
				Hardware: n.Hardware,
			}
			byID[n.ServerID] = g
			order = append(order, n.ServerID)
		}
		_, sel := selected[key]
		leaseIP := leaseByMAC[key]
		row := hostNICRow{
			MAC:       n.MAC,
			NIC:       n.NICName,
			Selected:  sel,
			ReserveIP: reserveByMAC[key],
			LeaseIP:   leaseIP,
			HasLease:  leaseIP != "",
		}
		g.NICs = append(g.NICs, row)
		g.NICCount++
		if sel {
			g.AllowedCount++
		}
		if row.HasLease {
			g.LeaseCount++
			g.HasLease = true
			g.Open = true
		}
	}

	data.HostServers = nil
	data.HostNICCount = 0
	data.HostBoundCount = 0
	groups := make([]hostServerGroup, 0, len(order)+1)
	for _, id := range order {
		g := byID[id]
		if g.LeaseCount > 0 {
			ips := make([]string, 0, g.LeaseCount)
			for _, nic := range g.NICs {
				if nic.HasLease {
					ips = append(ips, nic.LeaseIP)
				}
			}
			g.LeaseSummary = strings.Join(ips, ", ")
			data.HostBoundCount++
		}
		data.HostNICCount += g.NICCount
		groups = append(groups, *g)
	}

	orphanKeys := make([]string, 0)
	for key := range leaseByMAC {
		if _, ok := knownKeys[key]; !ok {
			orphanKeys = append(orphanKeys, key)
		}
	}
	sort.Strings(orphanKeys)
	if len(orphanKeys) > 0 {
		g := hostServerGroup{
			Name:     "Leases without inventory",
			Hardware: "live DHCP bindings — Probe/Inventory not required",
			HasLease: true,
			Open:     true,
		}
		ips := make([]string, 0, len(orphanKeys))
		for _, key := range orphanKeys {
			_, sel := selected[key]
			leaseIP := leaseByMAC[key]
			g.NICs = append(g.NICs, hostNICRow{
				MAC:       key,
				Selected:  sel,
				ReserveIP: reserveByMAC[key],
				LeaseIP:   leaseIP,
				HasLease:  true,
			})
			g.NICCount++
			g.LeaseCount++
			if sel {
				g.AllowedCount++
			}
			ips = append(ips, leaseIP)
			knownKeys[key] = struct{}{}
		}
		g.LeaseSummary = strings.Join(ips, ", ")
		data.HostBoundCount++
		data.HostNICCount += g.NICCount
		groups = append(groups, g)
	}

	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].HasLease != groups[j].HasLease {
			return groups[i].HasLease
		}
		return strings.ToLower(groups[i].Name) < strings.ToLower(groups[j].Name)
	})
	data.HostServers = groups

	var extra []string
	for _, m := range data.DHCP.AllowMACs {
		mac, err := normalizeMACInput(m)
		if err != nil || mac == "" {
			continue
		}
		if _, known := knownKeys[strings.ToLower(mac)]; !known {
			extra = append(extra, mac)
		}
	}
	data.AllowMACsText = strings.Join(extra, "\n")

	var resExtra []string
	for _, r := range data.DHCP.Reservations {
		mac, err := normalizeMACInput(r.MAC)
		if err != nil || mac == "" {
			continue
		}
		if _, known := knownKeys[strings.ToLower(mac)]; !known {
			resExtra = append(resExtra, mac+","+strings.TrimSpace(r.IP))
		}
	}
	data.ReservationsText = strings.Join(resExtra, "\n")
}

// enrichDataLeasesWith attaches server / hardware labels. Admin data-lease
// bindings win over inventory MAC lookup (manual assign / unassign).
func (s *Server) enrichDataLeasesWith(nics []inventoryNIC, data *pageData) {
	var bindings map[string]store.DataLeaseBinding
	if s.store != nil {
		bindings, _ = s.store.GetDataLeaseBindings()
	}
	hwByID := map[int64]string{}
	for _, n := range nics {
		if _, ok := hwByID[n.ServerID]; !ok {
			hwByID[n.ServerID] = n.Hardware
		}
	}
	for i := range data.Leases {
		macKey := strings.ToLower(strings.TrimSpace(data.Leases[i].MAC))
		if b, ok := bindings[macKey]; ok && b.ServerID > 0 {
			data.Leases[i].ServerID = b.ServerID
			name := b.ServerName
			if name == "" && s.store != nil {
				if srv, err := s.store.GetServer(b.ServerID); err == nil && srv != nil {
					name = srv.Name
				}
			}
			data.Leases[i].ServerName = name
			data.Leases[i].Hardware = hwByID[b.ServerID]
			data.Leases[i].NIC = "manual"
			continue
		}
		hit := lookupInventoryNIC(nics, data.Leases[i].MAC)
		if hit == nil {
			continue
		}
		data.Leases[i].ServerID = hit.ServerID
		data.Leases[i].ServerName = hit.ServerName
		data.Leases[i].Hardware = hit.Hardware
		data.Leases[i].NIC = hit.NICName
	}
}

func (s *Server) fillDHCPCommonTexts(data *pageData) {
	data.DNSJoined = strings.Join(data.DHCP.DNS, ", ")
	data.DenyMACsText = strings.Join(data.DHCP.DenyMACs, "\n")
	var resLines []string
	for _, r := range data.DHCP.Reservations {
		resLines = append(resLines, r.MAC+","+r.IP)
	}
	data.ReservationsText = strings.Join(resLines, "\n")
}

// dhcpFromForm parses shared DHCP fields. When withPXE is true (data plane),
// next-server and boot-file are included; OOB agent DHCP leaves them empty.
func dhcpFromForm(r *http.Request, withPXE bool) copper_dhcp.Config {
	cfg := copper_dhcp.Config{
		Enabled:    r.FormValue("enabled") == "1",
		Interface:  strings.TrimSpace(r.FormValue("interface")),
		Listen:     strings.TrimSpace(r.FormValue("listen")),
		ServerIP:   strings.TrimSpace(r.FormValue("server_ip")),
		SubnetCIDR: strings.TrimSpace(r.FormValue("subnet")),
		RangeStart: strings.TrimSpace(r.FormValue("range_start")),
		RangeEnd:   strings.TrimSpace(r.FormValue("range_end")),
		Gateway:    strings.TrimSpace(r.FormValue("gateway")),
		LeaseTime:  strings.TrimSpace(r.FormValue("lease_time")),
		AllowAll:   r.FormValue("allow_all") == "1",
	}
	if withPXE {
		cfg.NextServer = strings.TrimSpace(r.FormValue("next_server"))
		cfg.BootFile = strings.TrimSpace(r.FormValue("boot_file"))
	}
	for _, part := range strings.Split(r.FormValue("dns"), ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			cfg.DNS = append(cfg.DNS, part)
		}
	}

	seen := map[string]struct{}{}
	addMAC := func(raw string) {
		mac, err := normalizeMACInput(raw)
		if err != nil || mac == "" {
			return
		}
		key := strings.ToLower(mac)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		cfg.AllowMACs = append(cfg.AllowMACs, mac)
	}
	for _, m := range r.Form["allow_mac_known"] {
		addMAC(m)
	}
	for _, m := range splitLines(r.FormValue("allow_macs_extra")) {
		addMAC(m)
	}

	cfg.DenyMACs = splitLines(r.FormValue("deny_macs"))

	seenRes := map[string]struct{}{}
	addReservation := func(macRaw, ip string) {
		mac, err := normalizeMACInput(macRaw)
		ip = strings.TrimSpace(ip)
		if err != nil || mac == "" || ip == "" {
			return
		}
		key := strings.ToLower(mac)
		if _, ok := seenRes[key]; ok {
			return
		}
		seenRes[key] = struct{}{}
		cfg.Reservations = append(cfg.Reservations, copper_dhcp.Reservation{MAC: mac, IP: ip})
		// A reserved MAC should also be allowed unless allow_all is on.
		addMAC(mac)
	}
	// Data DHCP: per-inventory-NIC IP inputs (parallel reserve_mac / reserve_ip).
	if withPXE {
		macs := r.Form["reserve_mac"]
		ips := r.Form["reserve_ip"]
		for i, mac := range macs {
			ip := ""
			if i < len(ips) {
				ip = ips[i]
			}
			addReservation(mac, ip)
		}
	}
	for _, line := range splitLines(r.FormValue("reservations")) {
		mac, ip, ok := strings.Cut(line, ",")
		if !ok {
			continue
		}
		addReservation(mac, ip)
	}
	return cfg
}

// normalizeMACInput accepts empty (optional) or a parseable MAC; returns canonical form.
func normalizeMACInput(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	hw, err := net.ParseMAC(s)
	if err != nil {
		return "", fmt.Errorf("invalid MAC %q", s)
	}
	return hw.String(), nil
}

func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func urlQuery(s string) string {
	return url.QueryEscape(s)
}

// safeNextPath allows only same-origin relative paths for post-login redirects.
func safeNextPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "/"
	}
	if strings.Contains(raw, "://") {
		return "/"
	}
	pathOnly := raw
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		pathOnly = raw[:i]
	}
	switch pathOnly {
	case "/login", "/logout":
		return "/"
	}
	if strings.HasPrefix(pathOnly, "/api/") || strings.HasPrefix(pathOnly, "/static/") {
		return "/"
	}
	return raw
}

func (s *Server) serverFromPath(r *http.Request) (*store.Server, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	return s.store.GetServer(id)
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

func (s *Server) render(w http.ResponseWriter, page string, data pageData) {
	t := s.pages[page]
	if t == nil {
		http.Error(w, "template missing", 500)
		return
	}
	if data.StaticVersion == "" {
		data.StaticVersion = s.staticVer
	}
	s.fillAgentMeta(&data)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

// fillAgentMeta stamps mode / last-seen onto every page (poll uses heartbeat cache).
func (s *Server) fillAgentMeta(data *pageData) {
	if s.cfg == nil {
		return
	}
	data.AgentMode = s.cfg.AgentMode
	if data.AgentURL == "" {
		data.AgentURL = s.cfg.AgentBaseURL()
	}
	if !s.cfg.IsPollMode() || s.agent == nil {
		return
	}
	data.AgentURL = "poll mode (agent dials this control plane)"
	seen, osName, err := s.agent.LastSeen()
	if err != nil || seen.IsZero() {
		data.AgentLastSeen = "never"
		return
	}
	data.AgentLastSeen = seen.UTC().Format(time.RFC3339)
	data.AgentOS = osName
}

func (s *Server) isAuthed(r *http.Request) bool {
	c, err := r.Cookie("copper_session")
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(c.Value), []byte(s.sessionToken()))
}

func (s *Server) sessionTimeout() time.Duration {
	cfg, err := s.store.GetSessionSettings()
	if err != nil {
		cfg = store.DefaultSessionSettings()
	}
	hours := cfg.TimeoutHours
	if hours < store.MinSessionTimeoutHours {
		hours = store.DefaultSessionTimeoutHours
	}
	if hours > store.MaxSessionTimeoutHours {
		hours = store.MaxSessionTimeoutHours
	}
	return time.Duration(hours) * time.Hour
}

func (s *Server) issueSessionCookie(w http.ResponseWriter) {
	ttl := s.sessionTimeout()
	http.SetCookie(w, &http.Cookie{
		Name:     "copper_session",
		Value:    s.sessionToken(),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(ttl),
		MaxAge:   int(ttl.Seconds()),
	})
}

func (s *Server) sessionToken() string {
	mac := hmac.New(sha256.New, []byte(s.cfg.MasterPassword))
	_, _ = mac.Write([]byte("copper-control-plane-session-v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
