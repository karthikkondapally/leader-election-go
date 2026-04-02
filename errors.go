package pgelect

import "errors"

// Sentinel errors. Use errors.Is() to check for these.
var (
	// ErrStopped is returned by Start when the elector has already been stopped.
	ErrStopped = errors.New("pgelect: elector already stopped")

	// ErrInvalidConfig is returned by New when the Config fails validation.
	ErrInvalidConfig = errors.New("pgelect: invalid config")
)
