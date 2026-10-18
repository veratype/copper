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

	"copper_control_plane/internal/agent"
	"copper_control_plane/internal/config"
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
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
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

	_ = st.SetSetting("copper_bmc_host", cfg.CopperBMCHost)
	_ = st.SetSetting("copper_bmc_port", fmt.Sprintf("%d", cfg.CopperBMCPort))

	ag := agent.New(cfg.AgentBaseURL())

	localDHCP := copper_dhcp.NewManager()
	localDHCP.SetLogger(log.Default())
	if saved, err := st.GetLocalDHCPConfig(); err != nil {
		log.Printf("local dhcp: load config: %v", err)
	} else if saved.Enabled {
		if err := localDHCP.Apply(saved); err != nil {
			log.Printf("local dhcp: apply on startup failed (fix via Data DHCP UI): %v", err)
		} else {
			log.Printf("local dhcp: listening from saved config")
		}
	}

	staticRoot, err := fs.Sub(web.StaticFS, "static")
	if err != nil {
		return err
	}
	ui, err := web.New(cfg, st, ag, localDHCP, web.TemplatesFS, staticRoot)
	if err != nil {
		return err
	}
	ui.StartAgentWatch()

	fmt.Printf("Web UI listening on http://%s/\n", cfg.Listen)
	fmt.Printf("SQLite database: %s\n", dbPath)
	fmt.Printf("copper_bmc agent: %s\n", cfg.AgentBaseURL())
	fmt.Println("Login with master_password from config.yaml")
	fmt.Println("Data DHCP: configure under /dhcp/data (PXE next-server + boot file)")
	fmt.Println("OOB DHCP: auto-repushes saved policy when copper_bmc returns without DHCP running")
	log.Printf("control plane ready")
	return http.ListenAndServe(cfg.Listen, ui.Handler())
}
