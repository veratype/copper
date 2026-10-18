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

type Config struct {
	MasterPassword string `yaml:"master_password"`
	Listen         string `yaml:"listen"`
	Database       string `yaml:"database"`
	CopperBMCHost  string `yaml:"copper_bmc_host"`
	CopperBMCPort  int    `yaml:"copper_bmc_port"`
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
	return &cfg, nil
}

// PromptAgent asks for copper_bmc host/port when not set in config.
func (c *Config) PromptAgent(in *bufio.Reader) error {
	fmt.Println("=== copper_control_plane ===")
	fmt.Println("Configure the copper_bmc agent this control plane will talk to.")
	fmt.Println()

	if c.CopperBMCHost == "" {
		for {
			fmt.Print("copper_bmc IP / hostname: ")
			line, err := readLine(in)
			if err != nil {
				return err
			}
			if line == "" {
				fmt.Println("  Required.")
				continue
			}
			c.CopperBMCHost = line
			break
		}
	} else {
		fmt.Printf("copper_bmc host (from config): %s\n", c.CopperBMCHost)
	}

	fmt.Printf("copper_bmc command port [%d]: ", c.CopperBMCPort)
	line, err := readLine(in)
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
	return "http://" + net.JoinHostPort(c.CopperBMCHost, strconv.Itoa(c.CopperBMCPort))
}

func readLine(in *bufio.Reader) (string, error) {
	line, err := in.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
