package copper_dhcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// LoadConfigFile reads a previously saved Config JSON. Missing file → (zero, false, nil).
func LoadConfigFile(path string) (Config, bool, error) {
	if path == "" {
		return Config{}, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, false, nil
		}
		return Config{}, false, fmt.Errorf("read dhcp config file: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, false, fmt.Errorf("parse dhcp config file: %w", err)
	}
	return cfg, true, nil
}

// SaveConfigFile writes cfg as indented JSON (atomic replace).
func SaveConfigFile(path string, cfg Config) error {
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
