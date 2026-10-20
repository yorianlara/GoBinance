package persistence

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mi-bot/internal/domain"
)

// CurrentVersion identifica el esquema del archivo bots.json.
// Se incrementará cuando cambie el formato y haya que migrar.
const CurrentVersion = 1

// fileFormat es la estructura raíz del archivo bots.json.
type fileFormat struct {
	Version int          `json:"version"`
	Bots    []domain.Bot `json:"bots"`
}

// Store persiste los bots en un archivo JSON local.
// No tiene estado mutable propio: es seguro compartirlo entre goroutines
// siempre que el llamador coordine las escrituras (lo hace el BotManager).
type Store struct {
	path string
}

// New valida que el directorio exista y devuelve un Store listo.
// No crea el archivo: eso ocurre en el primer Save.
func New(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("la ruta del archivo de persistencia es obligatoria")
	}

	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("acceder al directorio %q: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("la ruta %q no es un directorio", dir)
	}

	return &Store{path: path}, nil
}

// Path devuelve la ruta del archivo gestionado.
func (s *Store) Path() string {
	return s.path
}

// Exists indica si el archivo ya está en disco.
func (s *Store) Exists() bool {
	_, err := os.Stat(s.path)
	return err == nil
}

// Load lee todos los bots del archivo.
// Si el archivo no existe, devuelve un slice vacío sin error.
func (s *Store) Load() ([]domain.Bot, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []domain.Bot{}, nil
		}
		return nil, fmt.Errorf("leer %q: %w", s.path, err)
	}

	var ff fileFormat
	if err := json.Unmarshal(data, &ff); err != nil {
		return nil, fmt.Errorf("interpretar %q: %w", s.path, err)
	}

	if ff.Version == 0 {
		return nil, fmt.Errorf("%q: falta el campo 'version'", s.path)
	}
	if ff.Version != CurrentVersion {
		return nil, fmt.Errorf(
			"%q: versión de esquema %d no soportada (se esperaba %d)",
			s.path, ff.Version, CurrentVersion,
		)
	}

	seen := make(map[string]struct{}, len(ff.Bots))
	for i := range ff.Bots {
		b := &ff.Bots[i]
		if err := b.Validate(); err != nil {
			return nil, fmt.Errorf("%q: bot %q inválido: %w", s.path, b.ID, err)
		}
		if _, dup := seen[b.ID]; dup {
			return nil, fmt.Errorf("%q: ID de bot duplicado %q", s.path, b.ID)
		}
		seen[b.ID] = struct{}{}
	}

	return ff.Bots, nil
}

// Save escribe los bots de forma atómica.
// Escribe a un temporal en el mismo directorio, hace fsync y renombra.
// Si algo falla a mitad, el archivo original queda intacto.
func (s *Store) Save(bots []domain.Bot) error {
	// Validar antes de tocar el disco.
	seen := make(map[string]struct{}, len(bots))
	for i := range bots {
		b := &bots[i]
		if err := b.Validate(); err != nil {
			return fmt.Errorf("bot %q inválido: %w", b.ID, err)
		}
		if _, dup := seen[b.ID]; dup {
			return fmt.Errorf("ID de bot duplicado %q", b.ID)
		}
		seen[b.ID] = struct{}{}
	}

	// Serializar con indentación legible.
	payload := fileFormat{Version: CurrentVersion, Bots: bots}
	if payload.Bots == nil {
		payload.Bots = []domain.Bot{}
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("serializar bots: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("crear temporal en %q: %w", dir, err)
	}
	tmpName := tmp.Name()

	// Limpieza best-effort si algo falla antes del rename.
	defer func() {
		_ = os.Remove(tmpName)
	}()

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

	// fsync del directorio para asegurar la entrada de rename.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	return nil
}
