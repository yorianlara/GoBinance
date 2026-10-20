package web

import (
	"fmt"
	"os"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/session"
	"github.com/gofiber/storage/postgres/v3"
)

// authConfig agrupa las credenciales y el secreto de sesión.
type authConfig struct {
	User     string
	Password string
	Enabled  bool
}

func loadAuthConfig() authConfig {
	user := os.Getenv("DASHBOARD_USER")
	pass := os.Getenv("DASHBOARD_PASSWORD")
	return authConfig{
		User:     user,
		Password: pass,
		Enabled:  user != "" && pass != "",
	}
}

// newSessionStore crea el store de sesiones respaldado por PostgreSQL.
func newSessionStore(dbURL string) (*session.Store, error) {
	storage := postgres.New(postgres.Config{
		ConnectionURI: dbURL,
		Table:         "fiber_sessions",
	})
	store := session.New(session.Config{
		Storage:    storage,
		Expiration: 24 * 60 * 60 * 1000 * 1000 * 1000, // 24h en ns
	})
	return store, nil
}

// requireAuth protege las rutas. Si la auth está deshabilitada, deja pasar.
func requireAuth(cfg authConfig, store *session.Store) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if !cfg.Enabled {
			return c.Next()
		}
		sess, err := store.Get(c)
		if err != nil {
			return c.Redirect("/login")
		}
		if sess.Get("user") == nil {
			return c.Redirect("/login")
		}
		return c.Next()
	}
}

// handleLoginPage muestra el formulario.
func (s *Server) handleLoginPage(c *fiber.Ctx) error {
	if !s.auth.Enabled {
		return c.Redirect("/")
	}
	// Si ya está logueado, redirigir al dashboard.
	sess, err := s.sessionStore.Get(c)
	if err == nil && sess.Get("user") != nil {
		return c.Redirect("/")
	}
	return s.renderLogin(c, "")
}

// handleLoginSubmit valida credenciales y crea la sesión.
func (s *Server) handleLoginSubmit(c *fiber.Ctx) error {
	if !s.auth.Enabled {
		return c.Redirect("/")
	}
	user := c.FormValue("user")
	pass := c.FormValue("password")

	if user != s.auth.User || pass != s.auth.Password {
		return s.renderLogin(c, "Usuario o contraseña incorrectos")
	}

	sess, err := s.sessionStore.Get(c)
	if err != nil {
		return s.renderLogin(c, fmt.Sprintf("Error de sesión: %v", err))
	}
	sess.Set("user", user)
	if err := sess.Save(); err != nil {
		return s.renderLogin(c, fmt.Sprintf("Error al guardar sesión: %v", err))
	}
	return c.Redirect("/")
}

// handleLogout destruye la sesión y redirige al login.
func (s *Server) handleLogout(c *fiber.Ctx) error {
	sess, err := s.sessionStore.Get(c)
	if err == nil {
		_ = sess.Destroy()
	}
	return c.Redirect("/login")
}

// renderLogin renderiza el formulario (sin layout completo, página independiente).
func (s *Server) renderLogin(c *fiber.Ctx, errMsg string) error {
	c.Set("Content-Type", "text/html; charset=utf-8")
	return s.pages.ExecuteTemplate(c, "login", map[string]any{"Error": errMsg})
}

// sessionStoreFromDB construye el store de sesiones con la URL de DATABASE_URL.
func sessionStoreFromDB() (*session.Store, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL no definida; no se puede usar sesiones con PostgreSQL")
	}
	// El driver de gofiber/storage/postgres/v3 espera un pgxpool o ConnectionURI.
	// Usamos ConnectionURI directamente.
	storage := postgres.New(postgres.Config{
		ConnectionURI: dbURL,
		Table:         "fiber_sessions",
	})
	store := session.New(session.Config{
		Storage: storage,
	})
	return store, nil
}

// helper para no importar strings si no se usa
var _ = strings.TrimSpace
