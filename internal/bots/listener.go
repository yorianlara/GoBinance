package bots

// OrderLifecycleListener permite a las estrategias sincronizar su estado
// interno con la aceptación, rechazo y cierre de sus órdenes.
type OrderLifecycleListener interface {
	OnOrderRejected()
	OnOrderClosed(filled bool)
}
