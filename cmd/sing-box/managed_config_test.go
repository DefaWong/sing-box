package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
	"github.com/stretchr/testify/require"
)

func TestSingleFileConfigStoreConditionalSaveAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	previous := []byte("{\n  \"log\": {}\n}\n")
	require.NoError(t, os.WriteFile(path, previous, 0o600))
	tracker := &managedReloadResult{}
	validations := 0
	store := &singleFileConfigStore{path: path, reload: tracker, validate: func([]byte) error {
		validations++
		return nil
	}}
	_, err := store.Save([]byte("stale"), []byte(`{"log":{}}`))
	require.ErrorIs(t, err, adapter.ErrConfigConflict)
	require.Zero(t, validations)

	next := []byte("{\n  \"log\": {\"level\": \"debug\"}\n}\n")
	revision, err := store.Save([]byte(managedConfigRevision(previous)), next)
	require.NoError(t, err)
	require.Equal(t, managedConfigRevision(next), revision)
	require.Equal(t, revision, tracker.Pending())
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, next, content)
	_, err = store.Save([]byte(managedConfigRevision(next)), []byte(`{"log":{}}`))
	require.ErrorIs(t, err, adapter.ErrConfigReloadInProgress)
	require.NoError(t, store.Rollback())
	content, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, previous, content)
	tracker.Finish(revision, nil)

	store.validate = func([]byte) error { return errors.New("provider startup failed") }
	_, err = store.Save([]byte(managedConfigRevision(previous)), next)
	require.ErrorContains(t, err, "provider startup failed")
	content, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, previous, content)
}

func TestValidateManagedConfigurationDownloadsDirectProvider(t *testing.T) {
	oldContext := globalCtx
	globalCtx = include.Context(context.Background())
	t.Cleanup(func() { globalCtx = oldContext })
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/bad" {
			http.Error(w, "bad subscription", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"outbounds":[{"type":"socks","tag":"fixture-main","server":"127.0.0.1","server_port":2000},{"type":"socks","tag":"fixture-alt","server":"127.0.0.1","server_port":2020}]}`)
	}))
	defer server.Close()
	configuration := func(url string) []byte {
		return []byte(fmt.Sprintf(`{
		  "outbounds": [
		    {"type":"selector","tag":"proxy","outbounds":["proxy-main"],"providers":["fixture"],"default":"proxy-main"},
		    {"type":"socks","tag":"proxy-main","server":"127.0.0.1","server_port":2000}
		  ],
		  "providers": [{"type":"remote","tag":"fixture","url":%q,"http_client":{},"update_interval":"24h"}],
		  "route": {"final":"proxy"}
		}`, url))
	}
	require.NoError(t, validateManagedConfiguration(configuration(server.URL)))
	require.Equal(t, int32(1), requests.Load())
	err := validateManagedConfiguration(configuration(server.URL + "/bad"))
	require.ErrorContains(t, err, "initial outbound provider")
	require.Equal(t, int32(2), requests.Load())
}

func TestManagedConfigurationRetainsExplicitDirectProviderHTTPClient(t *testing.T) {
	oldContext := globalCtx
	globalCtx = include.Context(context.Background())
	t.Cleanup(func() { globalCtx = oldContext })
	options, err := json.UnmarshalExtendedContext[option.Options](globalCtx, []byte(`{
	  "providers": [{"type":"remote","tag":"fixture","url":"http://127.0.0.1:18080/sub.json","http_client":{}}]
	}`))
	require.NoError(t, err)
	providerOptions := options.Providers[0].Options.(*option.ProviderRemoteOptions)
	require.NotNil(t, providerOptions.HTTPClient)
	require.Empty(t, providerOptions.HTTPClient.Detour)
}
