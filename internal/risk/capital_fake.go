package risk

import "context"

// FakeCapitalProvider es un CapitalProvider controlable para tests.
// También se puede usar desde otros paquetes (manager, bots) en sus tests.
type FakeCapitalProvider struct {
	Value float64
	Err   error
}

func (f *FakeCapitalProvider) CapitalUSDT(ctx context.Context) (float64, error) {
	if f == nil {
		return 0, nil
	}
	return f.Value, f.Err
}
