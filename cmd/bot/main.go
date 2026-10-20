package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"mi-bot/internal/bots"
	"mi-bot/internal/config"
	dbpkg "mi-bot/internal/db"
	"mi-bot/internal/domain"
	"mi-bot/internal/exchange"
	"mi-bot/internal/manager"
	"mi-bot/internal/persistence"
	"mi-bot/internal/registry"
	"mi-bot/internal/risk"
	"mi-bot/internal/web"
)

type binanceMarket struct{ client *exchange.BinanceClient }

func (b binanceMarket) Price(ctx context.Context, symbol string) (float64, error) {
	return b.client.GetSymbolPrice(ctx, symbol)
}
func (b binanceMarket) Klines(ctx context.Context, symbol, interval string, limit int) ([]bots.Candle, error) {
	raw, err := b.client.GetKlines(ctx, symbol, interval, limit)
	if err != nil {
		return nil, err
	}
	out := make([]bots.Candle, len(raw))
	for i, c := range raw {
		out[i] = bots.Candle{OpenTime: c.OpenTime, CloseTime: c.CloseTime, Open: c.Open, High: c.High, Low: c.Low, Close: c.Close, Volume: c.Volume}
	}
	return out, nil
}

// binanceExecutor adapta *exchange.BinanceClient a manager.OrderExecutor.
type binanceExecutor struct{ client *exchange.BinanceClient }

func (b binanceExecutor) PlaceOrder(ctx context.Context, req exchange.OrderRequest) (exchange.OrderResponse, error) {
	return b.client.PlaceOrder(ctx, req)
}
func (b binanceExecutor) GetOrder(ctx context.Context, symbol string, orderID int64) (exchange.OrderResponse, error) {
	return b.client.GetOrder(ctx, symbol, orderID)
}
func (b binanceExecutor) GetTrades(ctx context.Context, symbol string, orderID int64) ([]exchange.Fill, error) {
	return b.client.GetTrades(ctx, symbol, orderID)
}

func main() {
	logger := log.New(os.Stdout, "[mi-bot] ", log.LstdFlags)
	logger.Println("===================================")
	logger.Println("Inicializando Multi-Bot Trading...")
	logger.Println("===================================")
	logger.Printf("Sistema operativo: %s", runtime.GOOS)
	logger.Printf("Arquitectura: %s", runtime.GOARCH)

	cfgStore, err := config.NewStore("config.yaml")
	if err != nil {
		logger.Fatalf("Error al cargar la configuración: %v", err)
	}
	cfgVal := cfgStore.Get()
	cfg := &cfgVal

	binanceClient, err := exchange.NewBinanceClient(cfg)
	if err != nil {
		logger.Fatalf("Error al inicializar Binance: %v", err)
	}

	ctxStartup, cancelStartup := context.WithTimeout(context.Background(), 15*time.Second)
	if err := binanceClient.Ping(ctxStartup); err != nil {
		cancelStartup()
		logger.Fatalf("Error de conectividad con Binance: %v", err)
	}
	logger.Println("Conexión con Binance Spot Testnet verificada.")
	cancelStartup()

	// --- Registro de órdenes: PostgreSQL si DATABASE_URL existe, memoria si no.
	var reg registry.OrderRegistry
	dbURL := os.Getenv(cfg.Database.URLEnv)
	if dbURL != "" {
		ctxDB, cancelDB := context.WithTimeout(context.Background(), 15*time.Second)
		db, err := dbpkg.Open(ctxDB, dbURL)
		cancelDB()
		if err != nil {
			logger.Fatalf("Error al conectar con PostgreSQL: %v", err)
		}
		defer db.Close()

		ctxMig, cancelMig := context.WithTimeout(context.Background(), 30*time.Second)
		if err := dbpkg.Migrate(ctxMig, db); err != nil {
			cancelMig()
			logger.Fatalf("Error al migrar esquema: %v", err)
		}
		cancelMig()

		pg, err := registry.NewPostgres(db)
		if err != nil {
			logger.Fatalf("Error al crear PostgresRegistry: %v", err)
		}
		reg = pg
		logger.Println("Registro de órdenes: PostgreSQL")
	} else {
		reg = registry.NewMemory()
		logger.Printf("Aviso: %s no definida; registro en memoria (se pierde al reiniciar).", cfg.Database.URLEnv)
	}

	// 1. Cargar bots ANTES de crear el CapitalProvider, para derivar símbolos.
	store, err := persistence.New("bots.json")
	if err != nil {
		logger.Fatalf("Error al abrir bots.json: %v", err)
	}
	storedBots, err := store.Load()
	if err != nil {
		logger.Fatalf("Error al cargar bots.json: %v", err)
	}

	// 2. Derivar símbolos únicos de los bots + lista por defecto.
	symbolSet := make(map[string]struct{})
	for _, s := range exchange.DefaultSymbols {
		symbolSet[s] = struct{}{}
	}
	for _, b := range storedBots {
		if b.Symbol != "" {
			symbolSet[b.Symbol] = struct{}{}
		}
	}
	symbols := make([]string, 0, len(symbolSet))
	for s := range symbolSet {
		symbols = append(symbols, s)
	}
	logger.Printf("Símbolos a valorar: %d", len(symbols))

	capitalProvider, err := risk.NewBinanceCapitalProvider(binanceClient, symbols)
	if err != nil {
		logger.Fatalf("Error al crear proveedor de capital: %v", err)
	}

	stateStore, err := risk.NewStateStore("risk-state.json")
	if err != nil {
		logger.Fatalf("Error al abrir risk-state.json: %v", err)
	}
	riskState, err := risk.NewStatefulManager(risk.New(), stateStore)
	if err != nil {
		logger.Fatalf("Error al crear StatefulManager: %v", err)
	}

	market := binanceMarket{client: binanceClient}
	executor := binanceExecutor{client: binanceClient}

	deps := manager.Deps{
		StrategyFactory: bots.DefaultFactory,
		Capital:         capitalProvider,
		RiskState:       riskState,
		Registry:        reg,
		ConfigStore:     cfgStore,
		Market:          market,
		Klines:          market,
		Executor:        executor,
	}

	botMgr, err := manager.New(deps)
	if err != nil {
		logger.Fatalf("Error al crear BotManager: %v", err)
	}

	toEnable := make([]string, 0)
	for _, b := range storedBots {
		if err := botMgr.Create(b); err != nil {
			logger.Printf("Aviso: no se pudo registrar bot %q: %v", b.ID, err)
			continue
		}
		if b.Status == domain.BotStatusRunning {
			toEnable = append(toEnable, b.ID)
		}
	}
	logger.Printf("Bots cargados desde disco: %d", len(storedBots))

	for _, id := range toEnable {
		if err := botMgr.Enable(id); err != nil {
			logger.Printf("Aviso: no se pudo reanudar bot %q: %v", id, err)
			continue
		}
		logger.Printf("Bot %q reanudado", id)
	}

	// Servidor web en goroutine.
	webServer, err := web.New(web.Deps{
		Bots:     botMgr,
		Risk:     riskState,
		Registry: reg,
		Capital:  capitalProvider,
		Config:   cfgStore,
	})
	if err != nil {
		logger.Fatalf("Error al crear servidor web: %v", err)
	}
	go func() {
		addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
		if err := webServer.Start(addr); err != nil {
			logger.Printf("Aviso: servidor web terminó: %v", err)
		}
	}()
	logger.Printf("Dashboard: http://%s:%d", cfg.Server.Host, cfg.Server.Port)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	logger.Printf("Señal recibida (%s), apagando...", sig)

	// Apagar servidor web antes que el manager.
	if err := webServer.Shutdown(); err != nil {
		logger.Printf("Aviso: shutdown web: %v", err)
	}

	botMgr.Shutdown()

	if err := riskState.Persist(); err != nil {
		logger.Printf("Aviso: no se pudo guardar risk-state.json: %v", err)
	} else {
		logger.Println("Estado de riesgo guardado en risk-state.json")
	}

	if err := cfgStore.Persist(); err != nil {
		logger.Printf("Aviso: no se pudo guardar config.yaml: %v", err)
	} else {
		logger.Println("Configuración guardada en config.yaml")
	}

	finalBots := botMgr.List()
	if err := store.Save(finalBots); err != nil {
		logger.Printf("Aviso: no se pudo guardar bots.json: %v", err)
	} else {
		logger.Printf("Estado guardado en bots.json (%d bots)", len(finalBots))
	}

	logger.Println("Apagado completado.")
}
