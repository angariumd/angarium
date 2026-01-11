package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type CLIConfig struct {
	ControllerURL string `yaml:"controller_url"`
	Token         string `yaml:"token"`
}

func LoadCLIConfig() (*CLIConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	configPath := filepath.Join(home, ".angarium.yaml")
	f, err := os.Open(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &CLIConfig{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var cfg CLIConfig
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func SaveCLIConfig(cfg *CLIConfig) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	configPath := filepath.Join(home, ".angarium.yaml")
	f, err := os.Create(configPath)
	if err != nil {
		return err
	}
	defer f.Close()

	return yaml.NewEncoder(f).Encode(cfg)
}
