package pgelect

import "time"

// Status is the role of an instance as stored in the lease table.
type Status string

const (
	StatusActive  Status = "active"
	StatusPassive Status = "passive"
)

// State is the current observable state of the Elector's internal loop.
type State int32

const (
	// StatePassive — running but does not hold the lock.
	StatePassive State = iota
	// StateLeader — currently holds the advisory lock.
	StateLeader
	// StateReconnecting — lost DB connection, retrying. Leadership NOT held.
	StateReconnecting
	// StateStopped — Stop() was called or run context was cancelled.
	StateStopped
)

func (s State) String() string {
	switch s {
	case StatePassive:
		return "passive"
	case StateLeader:
		return "leader"
	case StateReconnecting:
		return "reconnecting"
	case StateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// LeaseInfo is a snapshot of one row in the leader_leases table.
// Returned by Elector.Leases() — useful for dashboards and health checks.
type LeaseInfo struct {
	AppName    string
	InstanceID string
	Status     Status
	LastSeen   time.Time
}
