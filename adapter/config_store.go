package adapter

import "errors"

var (
	ErrConfigConflict         = errors.New("configuration changed on disk; reload and retry")
	ErrConfigReloadInProgress = errors.New("configuration reload already in progress")
)

// ConfigStore is the canonical on-disk configuration of a sing-box instance.
// Save validates content, checks the bytes read by the caller and persists
// atomically. It returns the revision whose reload the caller should track.
type ConfigStore interface {
	Load() ([]byte, error)
	Save(expected, content []byte) (revision string, err error)
}

// ManagedReloadResult lives in the process-wide service registry, not in one
// Box instance. The old API can acknowledge a save, and the replacement API
// can expose the result after the old HTTP server has been closed.
type ManagedReloadResult interface {
	Begin(revision string)
	Pending() string
	Finish(revision string, err error)
	Status() (revision string, reloading bool, err error)
}

// ConfigRollback restores the bytes preceding the last managed Save.
type ConfigRollback interface {
	Rollback() error
}
