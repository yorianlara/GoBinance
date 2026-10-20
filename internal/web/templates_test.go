package web

import (
	"bytes"
	"html/template"
	"testing"

	"mi-bot/internal/config"
	"mi-bot/internal/domain"
)

func TestDashboardTemplatesRender(t *testing.T) {
	pages, err := template.ParseFS(
		templatesFS,
		"templates/layout.html",
		"templates/index.html",
		"templates/config.html",
		"templates/login.html",
		"templates/partials/*.html",
	)
	if err != nil {
		t.Fatalf("ParseFS: %v", err)
	}

	var dashboard bytes.Buffer
	data := pageData{
		Title:   "Dashboard",
		BotList: botsResultData{},
		Metrics: metricsData{},
	}
	if err := pages.ExecuteTemplate(&dashboard, "layout", data); err != nil {
		t.Fatalf("renderizar dashboard: %v", err)
	}
	if bytes.Contains(dashboard.Bytes(), []byte("hx-on::")) {
		t.Fatal("el dashboard contiene hx-on::, no compatible con el uso configurado de HTMX")
	}
	if !bytes.Contains(dashboard.Bytes(), []byte(`hx-trigger="every 5s, botsChanged from:body"`)) {
		t.Fatal("falta el refresco inmediato de bots tras crear uno")
	}
	if !bytes.Contains(dashboard.Bytes(), []byte(`hx-post="/account/orders/cancel-all"`)) ||
		!bytes.Contains(dashboard.Bytes(), []byte("hx-confirm=")) ||
		!bytes.Contains(dashboard.Bytes(), []byte("incluidas las creadas fuera de mi-bot")) {
		t.Fatal("la acción de cancelar todas las órdenes debe advertir y pedir confirmación")
	}
	if !bytes.Contains(dashboard.Bytes(), []byte(`hx-post="/account/positions/close-all"`)) ||
		!bytes.Contains(dashboard.Bytes(), []byte("Cerrar todas las posiciones")) ||
		!bytes.Contains(dashboard.Bytes(), []byte("Esta acción no se puede deshacer")) {
		t.Fatal("el cierre global de posiciones debe advertir y pedir confirmación")
	}
	var botList bytes.Buffer
	if err := pages.ExecuteTemplate(&botList, "bots-lista", []domain.Bot{{
		ID: "bot-1", Name: "Bot 1", Strategy: domain.StrategyConfig{Type: "dca"},
		Status: domain.BotStatusStopped, AllocationPercent: 10,
	}}); err != nil {
		t.Fatalf("renderizar lista de bots: %v", err)
	}
	if !bytes.Contains(botList.Bytes(), []byte(`hx-post="/bots/bot-1/close-position"`)) {
		t.Fatal("falta la acción explícita para cerrar la posición del bot")
	}

	var feedback bytes.Buffer
	if err := pages.ExecuteTemplate(&feedback, "bot-form-feedback", botFeedbackData{
		Message: "No se pudo crear el bot.", IsError: true,
	}); err != nil {
		t.Fatalf("renderizar error de formulario: %v", err)
	}
	if !bytes.Contains(feedback.Bytes(), []byte(`role="alert"`)) {
		t.Fatal("el error del formulario debe anunciarse como alerta")
	}

	var configPage bytes.Buffer
	configData := struct {
		Title   string
		Risk    config.RiskConfig
		Success bool
		Error   string
	}{Title: "Configuración"}
	if err := pages.ExecuteTemplate(&configPage, "config-page", configData); err != nil {
		t.Fatalf("renderizar configuración: %v", err)
	}
}
