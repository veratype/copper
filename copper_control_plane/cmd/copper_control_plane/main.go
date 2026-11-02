package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"copper_control_plane/internal/activity"
	"copper_control_plane/internal/agent"
	"copper_control_plane/internal/config"
	"copper_control_plane/internal/datamedia"
	"copper_control_plane/internal/jobs"
	"copper_control_plane/internal/license"
	"copper_control_plane/internal/store"
	"copper_control_plane/internal/web"

	"copper_dhcp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Subcommands that must not require config.yaml / bring up the UI.
	if len(os.Args) >= 2 && os.Args[1] == "fingerprint" {
		return runFingerprint(os.Args[2:])
	}

	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	// Enterprise: verify config license (OSS Configure is a no-op). Silent if missing/invalid.
	license.Configure(cfg.License)
	if err := cfg.PromptAgent(bufio.NewReader(os.Stdin)); err != nil {
		return err
	}

	dbPath := cfg.Database
	if !filepath.IsAbs(dbPath) {
		dbPath = filepath.Join(filepath.Dir(*cfgPath), dbPath)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()

	_ = st.SetSetting("agent_mode", cfg.AgentMode)
	_ = st.SetSetting("copper_bmc_host", cfg.CopperBMCHost)
	_ = st.SetSetting("copper_bmc_port", fmt.Sprintf("%d", cfg.CopperBMCPort))

	var ag *agent.Client
	if cfg.IsPollMode() {
		bridge := agent.NewPollBridge(st)
		ag = agent.NewPoll(st, bridge)
	} else {
		ag = agent.New(cfg.AgentBaseURL())
	}

	localAct := activity.New(0)
	localDHCP := copper_dhcp.NewManager()
	localDHCP.SetLogger(localAct.Logger("dhcp"))
	localDHCP.SetLeaseBackend(st.NewDataDHCPLeaseBackend())
	if saved, err := st.GetLocalDHCPConfig(); err != nil {
		log.Printf("local dhcp: load config: %v", err)
	} else if saved.Enabled {
		if err := localDHCP.Apply(saved); err != nil {
			log.Printf("local dhcp: apply on startup failed (fix via Data DHCP UI): %v", err)
		} else {
			n := len(localDHCP.Leases())
			log.Printf("local dhcp: listening from saved config (%d lease(s) restored from database)", n)
		}
	}

	staticRoot, err := fs.Sub(web.StaticFS, "static")
	if err != nil {
		return err
	}

	dataMedia := datamedia.New(localAct)
	if saved, err := st.GetLocalDHCPConfig(); err == nil && saved.Interface != "" {
		dataMedia.SetIfaceHint(saved.Interface)
	}
	if dm, err := st.GetDataMedia(); err != nil {
		log.Printf("data media: load config: %v", err)
	} else if dm.Enabled {
		if err := dataMedia.Apply(datamedia.Config{
			Enabled: dm.Enabled, Port: dm.Port, Folder: dm.Folder, Bind: dm.Bind,
		}); err != nil {
			log.Printf("data media: start failed (fix via Settings): %v", err)
		} else {
			log.Printf("data media: serving %s on %s", dm.Folder, dataMedia.Status().Addr)
		}
	}
	jobReg := jobs.New(st, localAct)

	ui, err := web.New(cfg, st, ag, localDHCP, localAct, dataMedia, jobReg, web.TemplatesFS, staticRoot)
	if err != nil {
		return err
	}
	ui.StartAgentWatch()

	fmt.Printf("Web UI listening on http://%s/\n", cfg.Listen)
	fmt.Printf("SQLite database: %s\n", dbPath)
	if cfg.IsPollMode() {
		fmt.Println("Agent mode: poll — copper_bmc must dial this control plane")
		fmt.Println("  POST /api/agent/poll/heartbeat")
		fmt.Println("  POST /api/agent/poll/commands/next")
		fmt.Println("  POST /api/agent/poll/commands/{id}/result")
	} else {
		fmt.Printf("Agent mode: listen — dialing copper_bmc at %s\n", cfg.AgentBaseURL())
	}
	fmt.Println("Login with master_password from config.yaml")
	fmt.Println("Data DHCP: configure under /dhcp/data (PXE next-server + boot file)")
	fmt.Println("Data media: cloud images under Settings (data-plane HTTP)")
	fmt.Println("OOB DHCP: auto-repushes saved policy when copper_bmc returns without DHCP running")
	log.Printf("control plane ready")
	return http.ListenAndServe(cfg.Listen, ui.Handler())
}

func runFingerprint(args []string) error {
	fs := flag.NewFlagSet("fingerprint", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	quiet := fs.Bool("q", false, "print only the fingerprint id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	facts := license.Collect()
	if *quiet {
		fmt.Println(facts.ID())
		return nil
	}
	fmt.Print(license.FormatFacts(facts))
	return nil
}
