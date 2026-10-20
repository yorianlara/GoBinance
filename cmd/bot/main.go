package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
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
	"mi-bot/internal/observability"
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
func (b binanceExecutor) GetOrderByClientID(ctx context.Context, symbol, clientOrderID string) (exchange.OrderResponse, error) {
	return b.client.GetOrderByClientID(ctx, symbol, clientOrderID)
}
func (b binanceExecutor) CancelOrder(ctx context.Context, symbol string, orderID int64) error {
	return b.client.CancelOrder(ctx, symbol, orderID)
}
func (b binanceExecutor) GetTrades(ctx context.Context, symbol string, orderID int64) ([]exchange.Fill, error) {
	return b.client.GetTrades(ctx, symbol, orderID)
}
func (b binanceExecutor) GetFreeBalance(ctx context.Context, asset string) (float64, error) {
	return b.client.GetFreeBalance(ctx, asset)
}
func (b binanceExecutor) GetSymbolBaseAsset(ctx context.Context, symbol string) (string, error) {
	return b.client.GetSymbolBaseAsset(ctx, symbol)
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

	dbURL := os.Getenv(cfg.Database.URLEnv)
	if dbURL == "" {
		logger.Fatalf("%s no definida; PostgreSQL es obligatorio para persistir órdenes, sesiones y logs.", cfg.Database.URLEnv)
	}
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

	logHandler, err := observability.NewPostgresHandler(db)
	if err != nil {
		logger.Fatalf("Error al configurar logs PostgreSQL: %v", err)
	}
	consoleHandler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo, AddSource: true})
	slog.SetDefault(slog.New(observability.NewMultiHandler(consoleHandler, logHandler)).With("service", "mi-bot"))
	logger.SetOutput(slog.NewLogLogger(slog.Default().Handler(), slog.LevelInfo).Writer())
	log.SetOutput(logger.Writer())
	slog.Info("persistencia PostgreSQL y logging estructurado activados")
	slog.Info("application startup", "goos", runtime.GOOS, "goarch", runtime.GOARCH)
	fatalf := func(format string, args ...any) {
		logger.Printf(format, args...)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := logHandler.Close(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "No se pudo vaciar la cola de logs PostgreSQL: %v\n", err)
		}
		cancel()
		os.Exit(1)
	}

	binanceClient, err := exchange.NewBinanceClient(cfg)
	if err != nil {
		fatalf("Error al inicializar Binance: %v", err)
	}

	ctxStartup, cancelStartup := context.WithTimeout(context.Background(), 15*time.Second)
	if err := binanceClient.Ping(ctxStartup); err != nil {
		cancelStartup()
		fatalf("Error de conectividad con Binance: %v", err)
	}
	logger.Println("Conexión con Binance Spot Testnet verificada.")
	cancelStartup()

	// PostgreSQL es obligatorio para el registro durable de órdenes, sesiones y logs.
	pg, err := registry.NewPostgres(db)
	if err != nil {
		fatalf("Error al crear PostgresRegistry: %v", err)
	}
	var reg registry.OrderRegistry = pg
	logger.Println("Registro de órdenes: PostgreSQL")

	// 1. Cargar bots ANTES de crear el CapitalProvider, para derivar símbolos.
	store, err := persistence.New("bots.json")
	if err != nil {
		fatalf("Error al abrir bots.json: %v", err)
	}
	storedBots, err := store.Load()
	if err != nil {
		fatalf("Error al cargar bots.json: %v", err)
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
		fatalf("Error al crear proveedor de capital: %v", err)
	}

	stateStore, err := risk.NewStateStore("risk-state.json")
	if err != nil {
		fatalf("Error al abrir risk-state.json: %v", err)
	}
	riskState, err := risk.NewStatefulManager(risk.New(), stateStore)
	if err != nil {
		fatalf("Error al crear StatefulManager: %v", err)
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

	// Persistencia incremental: guardamos bots.json tras cada cambio de
	// catálogo o de estado, no solo al apagar, para no perder nada si el
	// proceso muere.
	var botMgr *manager.BotManager
	deps.PersistBots = func() error {
		return store.Save(botMgr.List())
	}

	botMgr, err = manager.New(deps)
	if err != nil {
		fatalf("Error al crear BotManager: %v", err)
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

	// Barrido de arranque: si una ejecución anterior se cayó, sus órdenes
	// pendientes podrían seguir vivas en Binance (reconcile solo las cancela
	// tras max_order_age_minutes). Se neutralizan ahora, contabilizando
	// fills parciales, sin cambiar el estado de los bots.
	ctxSweep, cancelSweep := context.WithTimeout(context.Background(), 2*time.Minute)
	swept, err := botMgr.CancelAllPendingOrders(ctxSweep)
	cancelSweep()
	if err != nil {
		logger.Printf("Aviso: barrido de arranque de órdenes pendientes: %v", err)
	} else if swept > 0 {
		logger.Printf("Barrido de arranque: órdenes pendientes neutralizadas para %d bot(s)", swept)
	}

	// Reconciliación de riesgo desde el registro (la contabilidad
	// autoritativa): recobrar exposición y posiciones abiertas para eliminar
	// huérfanas acumuladas por registros perdidos o ventas hechas fuera del
	// bot. Solo se ejecuta si TODO el registro es consistente.
	snapshots, err := botMgr.Inventories()
	if err != nil {
		logger.Printf("Aviso: reconstruir inventarios desde el registro: %v", err)
	} else {
		exposureBySymbol := make(map[string]float64, len(snapshots))
		openPositions := 0
		for _, snapshot := range snapshots {
			exposureBySymbol[snapshot.Symbol] += snapshot.CostBasisUSDT
			openPositions++
		}
		before, changed := riskState.ReconcileExposure(exposureBySymbol, openPositions)
		if changed {
			total := 0.0
			for _, v := range exposureBySymbol {
				total += v
			}
			logger.Printf("Reconciliación de riesgo: posiciones abiertas %d → %d; exposición %.2f → %.2f USDT; cambiado risk-state.json",
				before.OpenPositions, openPositions, before.ExposureTotalUSDT, total)
			if err := riskState.Persist(); err != nil {
				logger.Printf("Aviso: persistir estado de riesgo reconciliado: %v", err)
			}
		}
	}

	for _, id := range toEnable {
		if err := botMgr.Enable(id); err != nil {
			logger.Printf("Aviso: no se pudo reanudar bot %q: %v", id, err)
			continue
		}
		logger.Printf("Bot %q reanudado", id)
	}

	// Servidor web en goroutine.
	webServer, err := web.New(web.Deps{
		Bots:               botMgr,
		Risk:               riskState,
		Registry:           reg,
		Capital:            capitalProvider,
		Config:             cfgStore,
		OrderCanceller:     binanceClient,
		PositionCloser:     botMgr,
		AllPositionsCloser: botMgr,
	})
	if err != nil {
		fatalf("Error al crear servidor web: %v", err)
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

	// Una segunda señal corta el apagado en seco (salta la liquidación y el
	// vaciado que quedara pendiente).
	go func() {
		second := <-sigCh
		logger.Printf("Segunda señal (%s), cortando en seco...", second)
		os.Exit(2)
	}()

	// Apagar servidor web antes que el manager.
	if err := webServer.Shutdown(); err != nil {
		logger.Printf("Aviso: shutdown web: %v", err)
	}

	// Cierre completo del proceso: cancelar en Binance las órdenes pendientes
	// de todos los bots y liquidar todas las posiciones registradas, de modo
	// que tras apagar no quede nada vivo (ni posiciones abiertas ni órdenes
	// a la espera de max_order_age en el siguiente arranque).
	ctxClose, cancelClose := context.WithTimeout(context.Background(), 4*time.Minute)
	attempts, err := botMgr.CloseAllPositions(ctxClose)
	cancelClose()
	if err != nil {
		logger.Printf("Aviso: cierre de posiciones al apagar: %v", err)
	}
	sold, partial, noPosition, failed := 0, 0, 0, 0
	for _, attempt := range attempts {
		switch {
		case attempt.Err != nil:
			failed++
			logger.Printf("Aviso: liquidación al apagar %s/%s: vendidas %.8f; %v",
				attempt.BotID, attempt.Symbol, attempt.Result.SoldQuantity, attempt.Err)
			if attempt.Result.SoldQuantity > 0 {
				partial++
			}
		case attempt.Result.NoPosition:
			noPosition++
		default:
			if attempt.Result.Partial {
				partial++
			} else {
				sold++
			}
			logger.Printf("Liquidación al apagar %s/%s: vendidas %.8f %s por %.2f USDT; PnL %+.2f USDT",
				attempt.BotID, attempt.Symbol, attempt.Result.SoldQuantity, attempt.Symbol,
				attempt.Result.ProceedsUSDT, attempt.Result.RealizedPnLUSDT)
		}
	}
	logger.Printf("Cierre al apagar terminado: %d posición(es) vendida(s), %d parcial(es), %d sin posición y %d error(es).",
		sold, partial, noPosition, failed)

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
	ctxLogClose, cancelLogClose := context.WithTimeout(context.Background(), 10*time.Second)
	if err := logHandler.Close(ctxLogClose); err != nil {
		fmt.Fprintf(os.Stderr, "No se pudo vaciar la cola de logs PostgreSQL: %v\n", err)
	}
	cancelLogClose()
}
