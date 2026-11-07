package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
)

// singleFileConfigStore is shared by every replacement Box. Its lock covers
// the conditional write and Begin, so two API requests cannot queue reloads.
type singleFileConfigStore struct {
	path      string
	validate  func([]byte) error
	reload    adapter.ManagedReloadResult
	access    sync.RWMutex
	writeLock sync.Mutex
}

type managedReloadResult struct {
	access    sync.RWMutex
	revision  string
	reloading bool
	err       error
}

func (r *managedReloadResult) Begin(revision string) {
	r.access.Lock()
	defer r.access.Unlock()
	r.revision = revision
	r.reloading = true
	r.err = nil
}

func (r *managedReloadResult) Pending() string {
	r.access.RLock()
	defer r.access.RUnlock()
	if !r.reloading {
		return ""
	}
	return r.revision
}

func (r *managedReloadResult) Finish(revision string, err error) {
	r.access.Lock()
	defer r.access.Unlock()
	if r.revision != revision || !r.reloading {
		return
	}
	r.reloading = false
	r.err = err
}

func (r *managedReloadResult) Status() (string, bool, error) {
	r.access.RLock()
	defer r.access.RUnlock()
	return r.revision, r.reloading, r.err
}

func newManagedConfigServices() (*singleFileConfigStore, adapter.ManagedReloadResult) {
	result := &managedReloadResult{}
	if len(configPaths) != 1 || len(configDirectories) != 0 || configPaths[0] == "stdin" {
		return nil, result
	}
	return &singleFileConfigStore{
		path:     configPaths[0],
		validate: validateManagedConfiguration,
		reload:   result,
	}, result
}

func managedConfigRevision(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func (s *singleFileConfigStore) Load() ([]byte, error) {
	s.access.RLock()
	defer s.access.RUnlock()
	return s.load()
}

func (s *singleFileConfigStore) load() ([]byte, error) {
	info, err := os.Lstat(s.path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, E.New("configuration path is not a regular file: ", s.path)
	}
	return os.ReadFile(s.path)
}

func (s *singleFileConfigStore) Save(expected, content []byte) (string, error) {
	s.writeLock.Lock()
	defer s.writeLock.Unlock()
	s.access.Lock()
	defer s.access.Unlock()
	if _, reloading, _ := s.reload.Status(); reloading {
		return "", adapter.ErrConfigReloadInProgress
	}
	previous, err := s.load()
	if err != nil {
		return "", err
	}
	if managedConfigRevision(previous) != string(expected) {
		return "", adapter.ErrConfigConflict
	}
	// Provider download and outbound/provider startup happen before touching
	// either the canonical file or its recovery copy.
	if err := s.validate(content); err != nil {
		return "", E.Cause(err, "configuration startup validation failed")
	}
	if err := writeConfigAtomic(s.previousPath(), previous); err != nil {
		return "", E.Cause(err, "save previous configuration")
	}
	if err := writeConfigAtomic(s.path, content); err != nil {
		return "", err
	}
	revision := managedConfigRevision(content)
	s.reload.Begin(revision)
	return revision, nil
}

func (s *singleFileConfigStore) previousPath() string {
	return filepath.Join(filepath.Dir(s.path), "."+filepath.Base(s.path)+".atomdns-previous")
}

func (s *singleFileConfigStore) Rollback() error {
	s.writeLock.Lock()
	defer s.writeLock.Unlock()
	s.access.Lock()
	defer s.access.Unlock()
	previous, err := os.ReadFile(s.previousPath())
	if err != nil {
		return E.Cause(err, "read previous configuration")
	}
	return writeConfigAtomic(s.path, previous)
}

func writeConfigAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".atomdns-config.tmp-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(content)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

// validateManagedConfiguration starts a separate, listener-free instance far
// enough to initialize outbounds and download every provider. A temporary
// cache prevents a changed provider URL or an invalid response from being
// hidden by the production cache, and prevents cache.db lock conflicts.
func validateManagedConfiguration(content []byte) error {
	options, err := json.UnmarshalExtendedContext[option.Options](globalCtx, content)
	if err != nil {
		return E.Cause(err, "decode configuration")
	}
	temporaryDirectory, err := os.MkdirTemp("", "sing-box-atomdns-check-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporaryDirectory)
	options.Log = &option.LogOptions{Disabled: true}
	options.Inbounds = nil
	options.Endpoints = nil
	options.Services = nil
	options.NTP = nil
	options.NetworkNamespaces = nil
	if options.Route != nil {
		options.Route.Rules = nil
		options.Route.RuleSet = nil
	}
	options.Experimental = &option.ExperimentalOptions{
		CacheFile: &option.CacheFileOptions{
			Enabled: true,
			Path:    filepath.Join(temporaryDirectory, "cache.db"),
		},
	}
	ctx, cancel := context.WithTimeout(service.ExtendContext(globalCtx), 90*time.Second)
	defer cancel()
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		return E.Cause(err, "create validation service")
	}
	if err := instance.PreStart(); err != nil {
		_ = instance.Close()
		return E.Cause(err, "start outbounds and providers")
	}
	return instance.Close()
}

func rollbackManagedConfiguration(store adapter.ConfigStore, revision string, cause error) error {
	if store == nil || revision == "" {
		return cause
	}
	rollback, loaded := store.(adapter.ConfigRollback)
	if !loaded {
		return E.New(cause, "; configuration rollback is unavailable")
	}
	if err := rollback.Rollback(); err != nil {
		return E.New(cause, "; restore previous configuration failed: ", err)
	}
	return cause
}

func logManagedReloadFailure(err error) {
	log.Error(E.Cause(err, "managed configuration reload failed; restored previous instance"))
}
