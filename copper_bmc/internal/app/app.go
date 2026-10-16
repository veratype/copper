package app

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"copper_bmc/internal/command"
	"copper_bmc/internal/config"
	"copper_bmc/internal/media"
	"copper_bmc/internal/redfishx"
)

// Run starts both network planes:
//   - media   on OOB/BMC-facing NIC (hardware BMCs pull ISOs here)
//   - command on control-plane-facing NIC (copper_control_plane issues ops here)
func Run(cfg *config.Config) error {
	printBanner(cfg)

	rf := redfishx.New()
	mediaSrv := media.New(cfg)
	cmdAPI := command.New(cfg, rf)

	errCh := make(chan error, 2)
	go func() { errCh <- mediaSrv.ListenAndServe() }()
	go func() { errCh <- cmdAPI.ListenAndServe() }()

	err := <-errCh
	return fmt.Errorf("listener stopped: %w", err)
}

func printBanner(cfg *config.Config) {
	fmt.Println()
	fmt.Println("copper_bmc is up with split network planes:")
	fmt.Println()
	fmt.Println("  [OOB / BMC plane]  media file server")
	fmt.Printf("    listen : http://%s/\n", cfg.MediaListener.Addr())
	fmt.Printf("    nic    : %s\n", cfg.MediaListener.Interface)
	fmt.Printf("    folder : %s\n", cfg.MediaFolder)
	fmt.Println("    note   : image_url for virtual media must use an IP on this plane")
	fmt.Println()
	fmt.Println("  [control-plane]    command API (multi-BMC via per-request target)")
	fmt.Printf("    listen : http://%s/\n", cfg.CommandListener.Addr())
	fmt.Printf("    nic    : %s\n", cfg.CommandListener.Interface)
	fmt.Println("    routes :")
	fmt.Println("      GET  /v1/health")
	fmt.Println("      GET  /v1/system")
	fmt.Println("      GET  /v1/system/storage")
	fmt.Println("      GET  /v1/system/boot")
	fmt.Println("      POST /v1/system/boot")
	fmt.Println("      GET  /v1/system/virtual-media")
	fmt.Println("      POST /v1/system/virtual-media/mount")
	fmt.Println("      POST /v1/system/actions/reset")
	fmt.Println("    target : X-Copper-BMC-Endpoint / Username / Password / Insecure")
	fmt.Println()
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
	log.Println("Press Ctrl+C to stop.")
}
