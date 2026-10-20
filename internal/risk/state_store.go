package risk

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// StateStore persiste el State en un archivo JSON atómico.
type StateStore struct {
	path string
}

// NewStateStore valida que el directorio exista y devuelve el store.
func NewStateStore(path string) (*StateStore, error) {
	if path == "" {
		return nil, fmt.Errorf("la ruta del state es obligatoria")
	}
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("acceder al directorio %q: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("la ruta %q no es un directorio", dir)
	}
	return &StateStore{path: path}, nil
}

func (s *StateStore) Path() string { return s.path }

// Load lee el State del archivo. Si no existe, devuelve State vacío sin error.
func (s *StateStore) Load() (State, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewState(), nil
		}
		return State{}, fmt.Errorf("leer %q: %w", s.path, err)
	}

	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("interpretar %q: %w", s.path, err)
	}
	if st.ExposureBySymbol == nil {
		st.ExposureBySymbol = make(map[string]float64)
	}
	return st, nil
}

// Save escribe el State de forma atómica.
func (s *StateStore) Save(st State) error {
	if st.ExposureBySymbol == nil {
		st.ExposureBySymbol = make(map[string]float64)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("serializar state: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("crear temporal en %q: %w", dir, err)
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
		return fmt.Errorf("renombrar temporal a %q: %w", s.path, err)
	}
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
