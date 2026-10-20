package bots

// OrderLifecycleListener es una interfaz opcional que las estrategias
// pueden implementar para enterarse de que una orden suya se ha cerrado
// (filled, cancelled o failed). Grid la usa para permitir la siguiente.
type OrderLifecycleListener interface {
	OnOrderClosed()
}
