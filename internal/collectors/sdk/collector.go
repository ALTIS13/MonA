package sdk

import "context"

// Collector is a generic device collector interface.
type Collector interface {
	Collect(ctx context.Context) error
	Name() string
}
