package web

import (
	"crypto/subtle"
	"fmt"
	"log/slog"
	"os"
	"time"

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

// sessionStoreFromDB construye el store de sesiones con la URL de DATABASE_URL.
// La cookie de sesión se marca HttpOnly (no legible desde JavaScript) y
// SameSite=Lax (los navegadores no la envían en POST cross-site), endureciendo
// el dashboard frente a robo de sesión y CSRF.
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
		Storage:        storage,
		Expiration:     24 * time.Hour,
		CookieHTTPOnly: true,
		CookieSameSite: "Lax",
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
			slog.Error("auth: leer sesión", "err", err)
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
	if err != nil {
		slog.Warn("auth: leer sesión de login", "err", err)
	} else if sess.Get("user") != nil {
		return c.Redirect("/")
	}
	return s.renderLogin(c, "")
}

// handleLoginSubmit valida credenciales y crea la sesión. La comparación es
// en tiempo constante para no filtrar por timing la longitud o los caracteres
// acertados de usuario/contraseña.
func (s *Server) handleLoginSubmit(c *fiber.Ctx) error {
	if !s.auth.Enabled {
		return c.Redirect("/")
	}
	user := c.FormValue("user")
	pass := c.FormValue("password")

	okUser := subtle.ConstantTimeCompare([]byte(user), []byte(s.auth.User)) == 1
	okPass := subtle.ConstantTimeCompare([]byte(pass), []byte(s.auth.Password)) == 1

	if !okUser || !okPass {
		slog.Warn("auth: login rechazado", "username", user)
		return s.renderLogin(c, "Usuario o contraseña incorrectos")
	}

	sess, err := s.sessionStore.Get(c)
	if err != nil {
		slog.Error("auth: obtener sesión", "err", err)
		return s.renderLogin(c, fmt.Sprintf("Error de sesión: %v", err))
	}
	sess.Set("user", user)
	if err := sess.Save(); err != nil {
		slog.Error("auth: guardar sesión", "err", err)
		return s.renderLogin(c, fmt.Sprintf("Error al guardar sesión: %v", err))
	}
	slog.Info("auth: login correcto", "username", user)
	return c.Redirect("/")
}

// handleLogout destruye la sesión y redirige al login.
func (s *Server) handleLogout(c *fiber.Ctx) error {
	sess, err := s.sessionStore.Get(c)
	if err != nil {
		slog.Error("auth: obtener sesión para logout", "err", err)
	} else if err := sess.Destroy(); err != nil {
		slog.Error("auth: destruir sesión", "err", err)
	}
	slog.Info("auth: logout")
	return c.Redirect("/login")
}

// renderLogin renderiza el formulario (sin layout completo, página independiente).
func (s *Server) renderLogin(c *fiber.Ctx, errMsg string) error {
	c.Set("Content-Type", "text/html; charset=utf-8")
	return s.pages.ExecuteTemplate(c, "login", map[string]any{"Error": errMsg})
}
