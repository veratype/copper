package command

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"copper_bmc/internal/config"
	"copper_bmc/internal/redfishx"
)

// API is the northbound control-plane command listener.
// Each Redfish operation requires a BMC target (from headers or JSON body)
// so one agent can drive a collection of BMCs.
type API struct {
	cfg *config.Config
	rf  *redfishx.Client
}

func New(cfg *config.Config, rf *redfishx.Client) *API {
	return &API{cfg: cfg, rf: rf}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", a.handleHealth)
	mux.HandleFunc("GET /v1/system", a.handleSystem)
	mux.HandleFunc("GET /v1/system/storage", a.handleStorage)
	mux.HandleFunc("GET /v1/system/boot", a.handleBootGet)
	mux.HandleFunc("POST /v1/system/boot", a.handleBootSet)
	mux.HandleFunc("GET /v1/system/virtual-media", a.handleVirtualMediaList)
	mux.HandleFunc("POST /v1/system/virtual-media/mount", a.handleVirtualMediaMount)
	mux.HandleFunc("POST /v1/system/actions/reset", a.handleReset)
	return loggingMiddleware(mux)
}

func (a *API) ListenAndServe() error {
	ln, err := net.Listen("tcp", a.cfg.CommandListener.Addr())
	if err != nil {
		return err
	}
	log.Printf("[command] API on http://%s/ (control-plane, NIC=%s)",
		a.cfg.CommandListener.Addr(), a.cfg.CommandListener.Interface)
	return http.Serve(ln, a.Handler())
}

func (a *API) handleHealth(w http.ResponseWriter, _ *http.Request) {
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
		"note": "pass BMC target via X-Copper-BMC-* headers (or bmc JSON field on POST)",
	})
}

func (a *API) handleSystem(w http.ResponseWriter, r *http.Request) {
	t, err := targetFromRequest(r, nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	inv, err := a.rf.Inventory(t)
	if err != nil {
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
	st, err := a.rf.Storage(t)
	if err != nil {
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
	boot, err := a.rf.Boot(t)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, boot)
}

func (a *API) handleBootSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BMC *redfishx.Target         `json:"bmc"`
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
	boot, err := a.rf.SetBoot(t, body.SetBootRequest)
	if err != nil {
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
	vms, err := a.rf.ListVirtualMedia(t)
	if err != nil {
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
	result, err := a.rf.MountBootMedia(t, body.MountMediaRequest)
	if err != nil {
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
	result, err := a.rf.Reset(t, body.ResetRequest)
	if err != nil {
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

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[command] %s %s %s", r.RemoteAddr, r.Method, r.URL.Path)
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
