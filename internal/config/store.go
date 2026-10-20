package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"go.yaml.in/yaml/v3"
)

// Store mantiene la configuración en memoria con acceso concurrente
// y la persiste a disco de forma atómica.
type Store struct {
	mu   sync.RWMutex
	cfg  Config
	path string
}

// NewStore carga la configuración desde path y devuelve el Store listo.
func NewStore(path string) (*Store, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	return &Store{cfg: *cfg, path: path}, nil
}

// Get devuelve una copia de la configuración completa.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Risk devuelve una copia de la configuración de riesgo.
func (s *Store) Risk() RiskConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Risk
}

// UpdateRisk valida y reemplaza la configuración de riesgo.
// Persiste a disco de forma atómica.
func (s *Store) UpdateRisk(r RiskConfig) error {
	// Validar antes de tocar nada.
	probe := s.Get()
	probe.Risk = r
	if err := probe.Validate(); err != nil {
		return fmt.Errorf("risk inválido: %w", err)
	}

	s.mu.Lock()
	s.cfg.Risk = r
	s.mu.Unlock()

	return s.Persist()
}

// Persist escribe la configuración actual a disco de forma atómica.
func (s *Store) Persist() error {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("serializar config: %w", err)
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("crear temporal: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("escribir temporal: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sincronizar temporal: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cerrar temporal: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("renombrar temporal: %w", err)
	}

	// fsync del directorio para asegurar la entrada de rename.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
