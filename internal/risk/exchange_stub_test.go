package risk

import "mi-bot/internal/exchange"

// exchangeStub no hace nada; solo sirve para satisfacer el tipo en tests
// que no llegan a llamar a Binance.
type exchangeStub = exchange.BinanceClient
