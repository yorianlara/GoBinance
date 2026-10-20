package web

import (
	"os"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// TestAuthDisabled_SinCredenciales comprueba que sin DASHBOARD_USER/PASSWORD
// el dashboard está abierto y no requiere login.
func TestAuthDisabled_SinCredenciales(t *testing.T) {
	os.Unsetenv("DASHBOARD_USER")
	os.Unsetenv("DASHBOARD_PASSWORD")
	defer os.Unsetenv("DASHBOARD_USER")
	defer os.Unsetenv("DASHBOARD_PASSWORD")

	cfg := loadAuthConfig()
	if cfg.Enabled {
		t.Fatal("sin credenciales, Enabled debe ser false")
	}
}

// TestAuthEnabled_ConCredenciales comprueba que con DASHBOARD_USER/PASSWORD
// el sistema marca Enabled=true.
func TestAuthEnabled_ConCredenciales(t *testing.T) {
	os.Setenv("DASHBOARD_USER", "admin")
	os.Setenv("DASHBOARD_PASSWORD", "secret")
	defer os.Unsetenv("DASHBOARD_USER")
	defer os.Unsetenv("DASHBOARD_PASSWORD")

	cfg := loadAuthConfig()
	if !cfg.Enabled {
		t.Fatal("con credenciales, Enabled debe ser true")
	}
	if cfg.User != "admin" || cfg.Password != "secret" {
		t.Fatalf("credenciales inesperadas: %+v", cfg)
	}
}

// TestRequireAuth_DisabledDejaPasar comprueba que sin auth, el middleware
// deja pasar directamente.
func TestRequireAuth_DisabledDejaPasar(t *testing.T) {
	app := fiber.New()
	cfg := authConfig{Enabled: false}
	app.Use(requireAuth(cfg, nil))
	app.Get("/", func(c *fiber.Ctx) error { return c.SendString("ok") })

	resp, err := app.Test(mustReq(t, "GET", "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("se esperaba 200, hay %d", resp.StatusCode)
	}
}

// TestRequireAuth_EnabledSinSesionRedirige comprueba que con auth y sin sesión
// se redirige a /login.
func TestRequireAuth_EnabledSinSesionRedirige(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL no configurada; test de sesión con PostgreSQL omitido")
	}
	store, err := sessionStoreFromDB()
	if err != nil {
		t.Fatalf("sessionStoreFromDB: %v", err)
	}

	app := fiber.New()
	cfg := authConfig{Enabled: true, User: "admin", Password: "secret"}
	app.Use(requireAuth(cfg, store))
	app.Get("/", func(c *fiber.Ctx) error { return c.SendString("ok") })

	resp, err := app.Test(mustReq(t, "GET", "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusFound {
		t.Fatalf("se esperaba 302 (redirect a /login), hay %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Fatalf("Location inesperado: %q", loc)
	}
}

// TestLoadAuthConfig_ParcialSinPassword verifica que si falta el password,
// la auth queda deshabilitada.
func TestLoadAuthConfig_ParcialSinPassword(t *testing.T) {
	os.Setenv("DASHBOARD_USER", "admin")
	os.Unsetenv("DASHBOARD_PASSWORD")
	defer os.Unsetenv("DASHBOARD_USER")
	defer os.Unsetenv("DASHBOARD_PASSWORD")

	cfg := loadAuthConfig()
	if cfg.Enabled {
		t.Fatal("sin password, Enabled debe ser false")
	}
}

// TestLoadAuthConfig_ParcialSinUser verifica que si falta el usuario,
// la auth queda deshabilitada.
func TestLoadAuthConfig_ParcialSinUser(t *testing.T) {
	os.Unsetenv("DASHBOARD_USER")
	os.Setenv("DASHBOARD_PASSWORD", "secret")
	defer os.Unsetenv("DASHBOARD_USER")
	defer os.Unsetenv("DASHBOARD_PASSWORD")

	cfg := loadAuthConfig()
	if cfg.Enabled {
		t.Fatal("sin user, Enabled debe ser false")
	}
}
