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
	NoVerifyTLS bool   `yaml:"no_verify_tls"` // Skip TLS cert verification for agent calls (dev only)
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
	LogDir        string `yaml:"log_dir"`       // Directory for job logs; defaults to /var/log/angarium/jobs
	NoVerifyTLS   bool   `yaml:"no_verify_tls"` // Skip TLS cert verification for controller calls (dev only)
}

func LoadControllerConfig(path string) (*ControllerConfig, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) && path == "config/controller.yaml" {
		if _, err := os.Stat("/etc/angarium/controller.yaml"); err == nil {
			path = "/etc/angarium/controller.yaml"
		}
	}

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
	if _, err := os.Stat(path); os.IsNotExist(err) && path == "config/agent.yaml" {
		if _, err := os.Stat("/etc/angarium/agent.yaml"); err == nil {
			path = "/etc/angarium/agent.yaml"
		}
	}

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
