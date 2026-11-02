package config

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	AgentModeListen = "listen"
	AgentModePoll   = "poll"
)

type Config struct {
	MasterPassword string `yaml:"master_password"`
	Listen         string `yaml:"listen"`
	Database       string `yaml:"database"`
	// License is an enterprise certificate blob (copper1.…). No UI — config only.
	// Missing/invalid → hardware RAID stays invisible (no messages).
	License string `yaml:"license"`
	// AgentMode is listen (CP dials agent) or poll (agent dials CP).
	AgentMode     string `yaml:"agent_mode"`
	CopperBMCHost string `yaml:"copper_bmc_host"`
	CopperBMCPort int    `yaml:"copper_bmc_port"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.MasterPassword == "" {
		return nil, fmt.Errorf("master_password is required in config")
	}
	if cfg.Listen == "" {
		cfg.Listen = "0.0.0.0:8080"
	}
	if cfg.Database == "" {
		cfg.Database = "data/control_plane.db"
	}
	if cfg.CopperBMCPort == 0 {
		cfg.CopperBMCPort = 9090
	}
	cfg.AgentMode = strings.ToLower(strings.TrimSpace(cfg.AgentMode))
	if cfg.AgentMode == "" {
		cfg.AgentMode = AgentModeListen
	}
	if cfg.AgentMode != AgentModeListen && cfg.AgentMode != AgentModePoll {
		return nil, fmt.Errorf("agent_mode must be %q or %q", AgentModeListen, AgentModePoll)
	}
	return &cfg, nil
}

// PromptAgent asks for agent talk mode and listen-mode host/port when needed.
func (c *Config) PromptAgent(in *bufio.Reader) error {
	fmt.Println("=== copper_control_plane ===")
	fmt.Println("How will copper_bmc talk to this control plane?")
	fmt.Println("  listen — this process dials copper_bmc's command API (existing)")
	fmt.Println("  poll   — copper_bmc dials this process and pulls commands")
	fmt.Println()

	if c.AgentMode != AgentModeListen && c.AgentMode != AgentModePoll {
		c.AgentMode = AgentModeListen
	}

	fmt.Printf("Agent mode listen/poll [%s]: ", c.AgentMode)
	line, err := readLine(in)
	if err != nil {
		return err
	}
	if line != "" {
		switch strings.ToLower(line) {
		case AgentModeListen, "l":
			c.AgentMode = AgentModeListen
		case AgentModePoll, "p":
			c.AgentMode = AgentModePoll
		default:
			return fmt.Errorf("invalid agent mode %q (use listen or poll)", line)
		}
	}

	if c.AgentMode == AgentModePoll {
		fmt.Println()
		fmt.Println("Poll mode: do not set copper_bmc_host — the agent will check in.")
		fmt.Printf("Agent poll endpoints on this UI: http://<this-host>%s/api/agent/poll/...\n", listenPortSuffix(c.Listen))
		fmt.Println("BMC inventory & credentials are managed in the web UI after login.")
		fmt.Println()
		return nil
	}

	fmt.Println()
	fmt.Println("Configure the copper_bmc agent this control plane will dial.")
	if c.CopperBMCHost == "" {
		for {
			fmt.Print("copper_bmc IP / hostname: ")
			line, err := readLine(in)
			if err != nil {
				return err
			}
			if line == "" {
				fmt.Println("  Required in listen mode.")
				continue
			}
			c.CopperBMCHost = line
			break
		}
	} else {
		fmt.Printf("copper_bmc host (from config): %s\n", c.CopperBMCHost)
	}

	fmt.Printf("copper_bmc command port [%d]: ", c.CopperBMCPort)
	line, err = readLine(in)
	if err != nil {
		return err
	}
	if line != "" {
		port, err := strconv.Atoi(line)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("invalid port")
		}
		c.CopperBMCPort = port
	}

	fmt.Printf("Agent API base: http://%s\n", net.JoinHostPort(c.CopperBMCHost, strconv.Itoa(c.CopperBMCPort)))
	fmt.Println("BMC inventory & credentials are managed in the web UI after login.")
	fmt.Println()
	return nil
}

func (c *Config) AgentBaseURL() string {
	if c.AgentMode == AgentModePoll {
		return "poll://agent"
	}
	return "http://" + net.JoinHostPort(c.CopperBMCHost, strconv.Itoa(c.CopperBMCPort))
}

func (c *Config) IsPollMode() bool {
	return c.AgentMode == AgentModePoll
}

func listenPortSuffix(listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return ""
	}
	return ":" + port
}

func readLine(in *bufio.Reader) (string, error) {
	line, err := in.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
