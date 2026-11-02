package app

import (
	"fmt"
	"os"
	"path/filepath"

	"copper_bmc/internal/activity"
	"copper_bmc/internal/command"
	"copper_bmc/internal/config"
	"copper_bmc/internal/media"
	"copper_bmc/internal/netx"
	"copper_bmc/internal/poll"
	"copper_bmc/internal/redfishx"

	"copper_dhcp"
)

// Run starts media (+ optional DHCP/LLDP) and either:
//   - listen mode: command API for copper_control_plane to dial, or
//   - poll mode: outbound heartbeat + long-poll to copper_control_plane
func Run(cfg *config.Config) error {
	if cfg.Mode == "" {
		cfg.Mode = config.ModeListen
	}
	act := activity.New(0)
	printBanner(cfg, act)

	rf := redfishx.New()
	rf.SetLogger(func(level, format string, args ...any) {
		switch level {
		case "error":
			act.Error("redfish", format, args...)
		case "warn":
			act.Warn("redfish", format, args...)
		default:
			act.Info("redfish", format, args...)
		}
	})
	mediaSrv := media.New(cfg, act)

	var dhcpMgr *copper_dhcp.Manager
	if cfg.DHCPRole {
		dhcpMgr = copper_dhcp.NewManager()
		dhcpMgr.SetLogger(act.Logger("dhcp"))
		// Persist leases + last policy so a BMC restart restores DHCP without a manual push.
		leaseFile := filepath.Join("data", "oob_dhcp_leases.json")
		configFile := filepath.Join("data", "oob_dhcp_config.json")
		dhcpMgr.SetLeaseFile(leaseFile)
		dhcpMgr.SetConfigFile(configFile)
		if ok, err := dhcpMgr.ApplySaved(); err != nil {
			act.Warn("dhcp", "could not restore saved config from %s: %v — waiting for control plane push", configFile, err)
		} else if ok {
			st := dhcpMgr.Status()
			act.Info("dhcp", "restored saved config from %s (running=%v, leases=%s)", configFile, st.Running, leaseFile)
		} else {
			act.Info("dhcp", "DHCP role enabled — no saved config yet; waiting for copper_control_plane (leases: %s)", leaseFile)
		}
	} else {
		act.Info("app", "DHCP role disabled (Redfish agent only)")
	}

	var lldpTable *netx.LLDPTable
	var lldpErr string
	if !netx.LLDPSupported() {
		cfg.LLDPRole = false
		act.Info("lldp", "LLDP capture not available on this platform (compile-time stub) — skipping")
	} else if !cfg.LLDPRole {
		act.Info("lldp", "LLDP capture disabled by admin — skipping (no CAP_NET_RAW needed)")
	} else {
		act.Info("lldp", "init: media NIC=%q bind=%q (LLDPRole=true)",
			cfg.MediaListener.Interface, cfg.MediaListener.BindAddr)
		iface, err := netx.ResolveLLDPIface(cfg.MediaListener.Interface, cfg.MediaListener.BindAddr)
		if err != nil {
			lldpErr = err.Error()
			act.Error("lldp", "lldp init failed: %v — pick a concrete OOB media NIC (not 0.0.0.0/all)", err)
		} else {
			lldpTable = netx.NewLLDPTable()
			go func() {
				err := netx.ListenLLDP(iface, lldpTable, func(format string, args ...any) {
					act.Info("lldp", format, args...)
				})
				if err != nil {
					act.Error("lldp", "lldp listen failed: %v (agent keeps running; /v1/net/lldp stays up)", err)
				}
			}()
			act.Info("lldp", "passive capture on OOB iface %s (GET /v1/net/lldp)", iface)
		}
	}

	cmdAPI := command.New(cfg, rf, dhcpMgr, lldpTable, lldpErr, act)

	errCh := make(chan error, 2)
	go func() { errCh <- mediaSrv.ListenAndServe() }()

	if cfg.Mode == config.ModePoll {
		runner := poll.New(cfg, cmdAPI, dhcpMgr, lldpTable, lldpErr, act)
		go func() { errCh <- runner.Run() }()
	} else {
		go func() { errCh <- cmdAPI.ListenAndServe() }()
	}

	err := <-errCh
	return fmt.Errorf("listener stopped: %w", err)
}

func printBanner(cfg *config.Config, act *activity.Buffer) {
	fmt.Println()
	fmt.Println("copper_bmc is up with split network planes:")
	fmt.Println()
	fmt.Println("  [OOB / BMC plane]  media file server")
	fmt.Printf("    listen : http://%s/\n", cfg.MediaListener.Addr())
	fmt.Printf("    nic    : %s\n", cfg.MediaListener.Interface)
	fmt.Printf("    folder : %s\n", cfg.MediaFolder)
	if base := media.MediaBaseURL(cfg); base != "" {
		fmt.Printf("    base   : %s  ← BMC image_url must use this\n", base)
	} else {
		fmt.Println("    base   : (unknown — bind is 0.0.0.0; set a concrete OOB IP in image_url)")
	}
	fmt.Println("    note   : watch Activity/console for [media] TCP accept / REQ — silence means BMC never hit this listener")
	fmt.Println()
	if cfg.DHCPRole {
		fmt.Println("  [OOB / BMC plane]  DHCP role ENABLED")
		fmt.Println("    restores last config from data/oob_dhcp_config.json on startup")
		fmt.Println("    otherwise waits for config from copper_control_plane")
		fmt.Println("    PUT /v1/dhcp/config  GET /v1/dhcp/config  GET /v1/dhcp/leases")
		fmt.Println()
	} else {
		fmt.Println("  [OOB / BMC plane]  DHCP role disabled (Redfish agent only)")
		fmt.Println()
	}
	if cfg.LLDPRole {
		fmt.Println("  [OOB / BMC plane]  LLDP capture ENABLED")
		fmt.Println("    GET /v1/net/lldp  — chassis_id enrichment for inventory")
		fmt.Println("    needs CAP_NET_RAW / root on the media NIC")
		fmt.Println()
	} else {
		fmt.Println("  [OOB / BMC plane]  LLDP capture disabled")
		fmt.Println()
	}
	if cfg.Mode == config.ModePoll {
		fmt.Println("  [control-plane]    POLL mode (agent dials copper_control_plane)")
		fmt.Printf("    url    : %s\n", cfg.ControlPlaneURL)
		fmt.Println("    posts : /api/agent/poll/heartbeat")
		fmt.Println("    polls : /api/agent/poll/commands/next")
		fmt.Println("    note  : command API listen port is not opened")
		fmt.Println()
	} else {
		fmt.Println("  [control-plane]    command API LISTEN (multi-BMC via per-request target)")
		fmt.Printf("    listen : http://%s/\n", cfg.CommandListener.Addr())
		fmt.Printf("    nic    : %s\n", cfg.CommandListener.Interface)
		fmt.Println("    routes :")
		fmt.Println("      GET  /v1/health")
		fmt.Println("      GET  /v1/activity")
		fmt.Println("      GET  /v1/media/files")
		if cfg.LLDPRole {
			fmt.Println("      GET  /v1/net/lldp")
		}
		fmt.Println("      GET  /v1/system")
		fmt.Println("      GET  /v1/system/storage")
		fmt.Println("      GET  /v1/system/boot")
		fmt.Println("      POST /v1/system/boot")
		fmt.Println("      GET  /v1/system/virtual-media")
		fmt.Println("      POST /v1/system/virtual-media/mount")
		fmt.Println("      POST /v1/system/virtual-media/eject")
		fmt.Println("      POST /v1/system/actions/reset")
		fmt.Println("    target : X-Copper-BMC-Endpoint / Username / Password / Insecure")
		fmt.Println()
	}
	fmt.Println("Files on media plane:")
	_ = filepath.WalkDir(cfg.MediaFolder, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(cfg.MediaFolder, path)
		if relErr != nil {
			return nil
		}
		fmt.Printf("    /%s\n", filepath.ToSlash(rel))
		return nil
	})
	fmt.Println()
	act.Info("app", "Press Ctrl+C to stop.")
}
