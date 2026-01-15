package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type ControllerConfig struct {
	Addr        string `yaml:"addr"`
	DBPath      string `yaml:"db_path"`
	SharedToken string `yaml:"shared_token"` // For agent -> controller
	Users       []User `yaml:"users"`
	CertPath    string `yaml:"cert_path"`
	KeyPath     string `yaml:"key_path"`
}

type User struct {
	ID    string `yaml:"id"`
	Name  string `yaml:"name"`
	Token string `yaml:"token"`
}

type AgentConfig struct {
	ControllerURL string `yaml:"controller_url"`
	SharedToken   string `yaml:"shared_token"`
	NodeID        string `yaml:"node_id"`
	Addr          string `yaml:"addr"`
	CertPath      string `yaml:"cert_path"`
	KeyPath       string `yaml:"key_path"`
}

func LoadControllerConfig(path string) (*ControllerConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cfg ControllerConfig
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func LoadAgentConfig(path string) (*AgentConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cfg AgentConfig
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
