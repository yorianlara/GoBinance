package config

import "go.yaml.in/yaml/v3"

func yamlMarshal(cfg Config) ([]byte, error) {
	return yaml.Marshal(cfg)
}
