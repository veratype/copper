package command

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"copper_bmc/internal/activity"
	"copper_bmc/internal/config"
	"copper_bmc/internal/netx"
	"copper_bmc/internal/redfishx"

	"copper_dhcp"
)

// API is the northbound control-plane command listener.
// Each Redfish operation requires a BMC target (from headers or JSON body)
// so one agent can drive a collection of BMCs.
type API struct {
	cfg  *config.Config
	rf   *redfishx.Client
	dhcp *copper_dhcp.Manager // nil when DHCP role disabled at startup
	lldp *netx.LLDPTable      // nil when capture not started
	act  *activity.Buffer
}

func New(cfg *config.Config, rf *redfishx.Client, dhcp *copper_dhcp.Manager, lldp *netx.LLDPTable, act *activity.Buffer) *API {
	return &API{cfg: cfg, rf: rf, dhcp: dhcp, lldp: lldp, act: act}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", a.handleHealth)
	mux.HandleFunc("GET /v1/activity", a.handleActivity)
	mux.HandleFunc("GET /v1/system", a.handleSystem)
	mux.HandleFunc("GET /v1/system/storage", a.handleStorage)
	mux.HandleFunc("GET /v1/system/boot", a.handleBootGet)
	mux.HandleFunc("POST /v1/system/boot", a.handleBootSet)
	mux.HandleFunc("GET /v1/system/virtual-media", a.handleVirtualMediaList)
	mux.HandleFunc("POST /v1/system/virtual-media/mount", a.handleVirtualMediaMount)
	mux.HandleFunc("POST /v1/system/actions/reset", a.handleReset)
	mux.HandleFunc("GET /v1/dhcp/config", a.handleDHCPConfigGet)
	mux.HandleFunc("PUT /v1/dhcp/config", a.handleDHCPConfigPut)
	mux.HandleFunc("GET /v1/dhcp/leases", a.handleDHCPLeases)
	mux.HandleFunc("GET /v1/net/arp", a.handleARP)
	mux.HandleFunc("GET /v1/net/lldp", a.handleLLDP)
	return a.loggingMiddleware(mux)
}

func (a *API) ListenAndServe() error {
	ln, err := net.Listen("tcp", a.cfg.CommandListener.Addr())
	if err != nil {
		return err
	}
	a.act.Info("command", "API on http://%s/ (control-plane, NIC=%s)",
		a.cfg.CommandListener.Addr(), a.cfg.CommandListener.Interface)
	return http.Serve(ln, a.Handler())
}

func (a *API) handleHealth(w http.ResponseWriter, _ *http.Request) {
	dhcpInfo := map[string]any{
		"role_enabled": a.dhcp != nil,
	}
	if a.dhcp != nil {
		st := a.dhcp.Status()
		dhcpInfo["running"] = st.Running
		dhcpInfo["config_enabled"] = st.Config.Enabled
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"service": "copper_bmc",
		"planes": map[string]any{
			"media": map[string]any{
				"listen":    a.cfg.MediaListener.Addr(),
				"interface": a.cfg.MediaListener.Interface,
				"folder":    a.cfg.MediaFolder,
			},
			"command": map[string]any{
				"listen":    a.cfg.CommandListener.Addr(),
				"interface": a.cfg.CommandListener.Interface,
			},
		},
		"dhcp": dhcpInfo,
		"note": "pass BMC target via X-Copper-BMC-* headers (or bmc JSON field on POST)",
	})
}

func (a *API) handleActivity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var since uint64
	if v := strings.TrimSpace(q.Get("since")); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid since: %w", err))
			return
		}
		since = n
	}
	limit := 200
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid limit"))
			return
		}
		limit = n
	}
	component := strings.TrimSpace(q.Get("component"))

	events := a.act.Since(since, limit, component)
	var nextSince uint64 = since
	for _, ev := range events {
		if ev.ID > nextSince {
			nextSince = ev.ID
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events":     events,
		"next_since": nextSince,
	})
}

func (a *API) requireDHCPRole() error {
	if a.dhcp == nil {
		return fmt.Errorf("DHCP role is disabled on this agent (restart copper_bmc and enable DHCP role)")
	}
	return nil
}

func (a *API) handleDHCPConfigGet(w http.ResponseWriter, _ *http.Request) {
	if err := a.requireDHCPRole(); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	st := a.dhcp.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"role_enabled": true,
		"running":      st.Running,
		"config":       st.Config,
	})
}

func (a *API) handleDHCPConfigPut(w http.ResponseWriter, r *http.Request) {
	if err := a.requireDHCPRole(); err != nil {
		a.act.Warn("dhcp", "config put rejected: %v", err)
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var cfg copper_dhcp.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		a.act.Warn("dhcp", "config put invalid json: %v", err)
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid json: %w", err))
		return
	}
	a.act.Info("dhcp", "config put from control plane: enabled=%v iface=%q listen=%q pool=%s-%s allow_all=%v allow_macs=%d",
		cfg.Enabled, cfg.Interface, cfg.Listen, cfg.RangeStart, cfg.RangeEnd, cfg.AllowAll, len(cfg.AllowMACs))
	if err := a.dhcp.Apply(cfg); err != nil {
		a.act.Error("dhcp", "config apply failed: %v", err)
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	st := a.dhcp.Status()
	a.act.Info("dhcp", "config applied: running=%v", st.Running)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "applied",
		"running": st.Running,
		"config":  st.Config,
	})
}

func (a *API) handleDHCPLeases(w http.ResponseWriter, _ *http.Request) {
	if err := a.requireDHCPRole(); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"leases": a.dhcp.Leases(),
	})
}

// handleLLDP returns sighted LLDP neighbors on the OOB plane.
// GET /v1/net/lldp
// GET /v1/net/lldp?mac=aa:bb:…
// GET /v1/net/lldp?ip=192.168.10.20
// GET /v1/net/lldp?chassis_id=…
func (a *API) handleLLDP(w http.ResponseWriter, r *http.Request) {
	if a.lldp == nil {
		writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("LLDP capture is not running on this agent"))
		return
	}
	q := r.URL.Query()
	mac := strings.TrimSpace(q.Get("mac"))
	ip := strings.TrimSpace(q.Get("ip"))
	chassis := strings.TrimSpace(q.Get("chassis_id"))

	switch {
	case mac != "":
		n, ok := a.lldp.FindByMAC(mac)
		if !ok {
			writeErr(w, http.StatusNotFound, fmt.Errorf("no LLDP neighbor for mac %s", mac))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"neighbor": n})
	case ip != "":
		n, ok := a.lldp.FindByIP(ip)
		if !ok {
			writeErr(w, http.StatusNotFound, fmt.Errorf("no LLDP neighbor for ip %s", ip))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"neighbor": n})
	case chassis != "":
		n, ok := a.lldp.FindByChassisID(chassis)
		if !ok {
			writeErr(w, http.StatusNotFound, fmt.Errorf("no LLDP neighbor for chassis_id %s", chassis))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"neighbor": n})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"neighbors": a.lldp.List()})
	}
}

// handleARP resolves IP → MAC via the agent's neighbor table (OOB L2).
// GET /v1/net/arp?ip=192.168.10.20
func (a *API) handleARP(w http.ResponseWriter, r *http.Request) {
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	if ip == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("query param ip is required"))
		return
	}
	mac, err := netx.ResolveMAC(ip)
	if err != nil {
		a.act.Warn("net", "arp %s failed: %v", ip, err)
		writeErr(w, http.StatusNotFound, err)
		return
	}
	a.act.Info("net", "arp %s → %s", ip, mac)
	writeJSON(w, http.StatusOK, map[string]any{
		"ip":  ip,
		"mac": mac,
	})
}

func (a *API) handleSystem(w http.ResponseWriter, r *http.Request) {
	t, err := targetFromRequest(r, nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	a.act.Info("redfish", "inventory %s", t.Endpoint)
	inv, err := a.rf.Inventory(t)
	if err != nil {
		a.act.Error("redfish", "inventory %s failed: %v", t.Endpoint, err)
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

func (a *API) handleStorage(w http.ResponseWriter, r *http.Request) {
	t, err := targetFromRequest(r, nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	a.act.Info("redfish", "storage %s", t.Endpoint)
	st, err := a.rf.Storage(t)
	if err != nil {
		a.act.Error("redfish", "storage %s failed: %v", t.Endpoint, err)
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (a *API) handleBootGet(w http.ResponseWriter, r *http.Request) {
	t, err := targetFromRequest(r, nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	a.act.Info("redfish", "boot get %s", t.Endpoint)
	boot, err := a.rf.Boot(t)
	if err != nil {
		a.act.Error("redfish", "boot get %s failed: %v", t.Endpoint, err)
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, boot)
}

func (a *API) handleBootSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BMC *redfishx.Target `json:"bmc"`
		redfishx.SetBootRequest
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	t, err := targetFromRequest(r, body.BMC)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	a.act.Info("redfish", "boot set %s", t.Endpoint)
	boot, err := a.rf.SetBoot(t, body.SetBootRequest)
	if err != nil {
		a.act.Error("redfish", "boot set %s failed: %v", t.Endpoint, err)
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, boot)
}

func (a *API) handleVirtualMediaList(w http.ResponseWriter, r *http.Request) {
	t, err := targetFromRequest(r, nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	a.act.Info("redfish", "virtual-media list %s", t.Endpoint)
	vms, err := a.rf.ListVirtualMedia(t)
	if err != nil {
		a.act.Error("redfish", "virtual-media list %s failed: %v", t.Endpoint, err)
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"virtual_media": vms})
}

func (a *API) handleVirtualMediaMount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BMC *redfishx.Target `json:"bmc"`
		redfishx.MountMediaRequest
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	t, err := targetFromRequest(r, body.BMC)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	a.act.Info("redfish", "virtual-media mount %s image=%s", t.Endpoint, body.ImageURL)
	result, err := a.rf.MountBootMedia(t, body.MountMediaRequest)
	if err != nil {
		a.act.Error("redfish", "virtual-media mount %s failed: %v", t.Endpoint, err)
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (a *API) handleReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BMC *redfishx.Target `json:"bmc"`
		redfishx.ResetRequest
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	t, err := targetFromRequest(r, body.BMC)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	a.act.Info("redfish", "reset %s type=%s", t.Endpoint, body.ResetType)
	result, err := a.rf.Reset(t, body.ResetRequest)
	if err != nil {
		a.act.Error("redfish", "reset %s failed: %v", t.Endpoint, err)
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

// targetFromRequest prefers an explicit JSON bmc object, then falls back to headers:
//
//	X-Copper-BMC-Endpoint
//	X-Copper-BMC-Username
//	X-Copper-BMC-Password
//	X-Copper-BMC-Insecure  (true/false, default true)
func targetFromRequest(r *http.Request, bodyBMC *redfishx.Target) (redfishx.Target, error) {
	if bodyBMC != nil && bodyBMC.Endpoint != "" {
		t := *bodyBMC
		if r.Header.Get("X-Copper-BMC-Insecure") == "" && !t.Insecure {
			// default insecure for typical BMC self-signed certs when unset in body
			// only if caller didn't set insecure explicitly — leave as provided
		}
		return t, nil
	}

	endpoint := strings.TrimSpace(r.Header.Get("X-Copper-BMC-Endpoint"))
	username := strings.TrimSpace(r.Header.Get("X-Copper-BMC-Username"))
	password := r.Header.Get("X-Copper-BMC-Password")
	if endpoint == "" || username == "" {
		return redfishx.Target{}, fmt.Errorf("bmc target required: set X-Copper-BMC-Endpoint/Username/Password headers or JSON field \"bmc\"")
	}

	insecure := true
	if v := strings.TrimSpace(r.Header.Get("X-Copper-BMC-Insecure")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return redfishx.Target{}, fmt.Errorf("invalid X-Copper-BMC-Insecure: %w", err)
		}
		insecure = b
	}

	return redfishx.Target{
		Endpoint: endpoint,
		Username: username,
		Password: password,
		Insecure: insecure,
	}, nil
}

func (a *API) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Activity poll is high-frequency; skip request noise for it.
		if r.URL.Path != "/v1/activity" {
			a.act.Info("command", "%s %s %s", r.RemoteAddr, r.Method, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
