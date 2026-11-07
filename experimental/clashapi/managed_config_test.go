package clashapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

type testConfigStore struct {
	content []byte
	saves   int
	err     error
}

type testManagedRouter struct {
	adapter.Router
	reloaded int
}

func (r *testManagedRouter) Reload() { r.reloaded++ }

func (s *testConfigStore) Load() ([]byte, error) { return append([]byte(nil), s.content...), nil }
func (s *testConfigStore) Save(expected, content []byte) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if string(expected) != managedConfigRevision(s.content) {
		return "", adapter.ErrConfigConflict
	}
	s.content = append([]byte(nil), content...)
	s.saves++
	return managedConfigRevision(content), nil
}

func TestManagedConfigReadAndConditionalSave(t *testing.T) {
	t.Parallel()
	store := &testConfigStore{content: []byte(`{"log":{},"outbounds":[{"type":"direct","tag":"direct"}]}`)}
	router := &testManagedRouter{}
	server := &Server{
		ctx:    service.ContextWith[adapter.ConfigStore](context.Background(), store),
		router: router,
		logger: log.NewNOPFactory().NewLogger("test"),
	}
	handler := managedConfigRouter(server)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, response.Code)
	var current struct {
		Revision string          `json:"revision"`
		Config   json.RawMessage `json:"config"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &current))
	require.NotEmpty(t, current.Revision)

	request := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"revision":"stale","config":{}}`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusConflict, response.Code)
	require.Zero(t, store.saves)
	require.Zero(t, router.reloaded)

	request = httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"revision":"`+current.Revision+`","config":{"log":{},"outbounds":[{"type":"direct","tag":"direct"}]}}`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, 1, store.saves)
	require.Equal(t, 1, router.reloaded)
}

func TestManagedConfigRejectsInvalidAndUnavailableConfiguration(t *testing.T) {
	t.Parallel()
	server := &Server{ctx: context.Background(), router: &testManagedRouter{}, logger: log.NewNOPFactory().NewLogger("test")}
	response := httptest.NewRecorder()
	managedConfigRouter(server).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusNotImplemented, response.Code)

	store := &testConfigStore{content: []byte(`{"log":{}}`), err: errors.New("unknown field")}
	server.ctx = service.ContextWith[adapter.ConfigStore](context.Background(), store)
	response = httptest.NewRecorder()
	managedConfigRouter(server).ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"revision":"`+managedConfigRevision(store.content)+`","config":{"unknown":true}}`)))
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Zero(t, store.saves)

	store.err = errors.New("validation failed")
	response = httptest.NewRecorder()
	managedConfigRouter(server).ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"revision":"`+managedConfigRevision(store.content)+`","config":{"log":{}}}`)))
	require.Equal(t, http.StatusBadRequest, response.Code)
}

type testReloadTracker struct {
	revision  string
	reloading bool
	err       error
}

func (t *testReloadTracker) Begin(revision string) {
	t.revision, t.reloading, t.err = revision, true, nil
}
func (t *testReloadTracker) Pending() string {
	if t.reloading {
		return t.revision
	}
	return ""
}
func (t *testReloadTracker) Finish(revision string, err error) {
	if t.revision == revision {
		t.reloading, t.err = false, err
	}
}
func (t *testReloadTracker) Status() (string, bool, error) {
	return t.revision, t.reloading, t.err
}

func TestManagedConfigStatusAndParseNode(t *testing.T) {
	t.Parallel()
	store := &testConfigStore{content: []byte(`{"log":{}}`)}
	tracker := &testReloadTracker{}
	ctx := service.ContextWith[adapter.ConfigStore](context.Background(), store)
	ctx = service.ContextWith[adapter.ManagedReloadResult](ctx, tracker)
	server := &Server{ctx: ctx, router: &testManagedRouter{}, logger: log.NewNOPFactory().NewLogger("test")}
	handler := managedConfigRouter(server)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"revision":"`+managedConfigRevision(store.content)+`","config":{"log":{"level":"debug"}}}`)))
	require.Equal(t, http.StatusOK, response.Code)
	tracker.Begin(managedConfigRevision(store.content))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/status", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), `"reloading":true`)
	tracker.Finish(managedConfigRevision(store.content), errors.New("provider failed; restored"))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/status", nil))
	require.Contains(t, response.Body.String(), `"reloading":false`)
	require.Contains(t, response.Body.String(), "restored")
}

func TestManagedConfigRejectsReloadInProgress(t *testing.T) {
	t.Parallel()
	store := &testConfigStore{content: []byte(`{"log":{}}`), err: adapter.ErrConfigReloadInProgress}
	server := &Server{
		ctx:    service.ContextWith[adapter.ConfigStore](context.Background(), store),
		router: &testManagedRouter{}, logger: log.NewNOPFactory().NewLogger("test"),
	}
	response := httptest.NewRecorder()
	managedConfigRouter(server).ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"revision":"`+managedConfigRevision(store.content)+`","config":{}}`)))
	require.Equal(t, http.StatusConflict, response.Code)
}

func TestManagedConfigParsesCompleteNativeNode(t *testing.T) {
	t.Parallel()
	server := &Server{ctx: include.Context(context.Background())}
	for _, testCase := range []struct {
		name string
		body string
		tag  string
		kind string
	}{
		{"socks", `{"link":"socks5h://user:pass@192.0.2.1:1080#socks-node"}`, "socks-node", "socks"},
		{"vless", `{"link":"vless://11111111-1111-1111-1111-111111111111@192.0.2.1:443?security=tls&sni=example.com&type=ws&path=%2Fproxy#vless-node","tag":"manual-vless"}`, "manual-vless", "vless"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			managedConfigRouter(server).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/parse-node", strings.NewReader(testCase.body)))
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var node map[string]any
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &node))
			require.Equal(t, testCase.tag, node["tag"])
			require.Equal(t, testCase.kind, node["type"])
			require.NotEmpty(t, node["server"])
			if testCase.name == "socks" {
				require.Equal(t, "user", node["username"])
				require.Equal(t, "pass", node["password"])
			} else {
				require.NotEmpty(t, node["uuid"])
				require.Equal(t, "example.com", node["tls"].(map[string]any)["server_name"])
			}
		})
	}
	for _, body := range []string{
		`{"link":"unknown://example.com#bad"}`,
		`{"link":"vless://one\nvless://two"}`,
		`{"link":"socks5h://192.0.2.1:1080"}`,
		`{"link":"socks5h://192.0.2.1:1080#bad/tag"}`,
	} {
		response := httptest.NewRecorder()
		managedConfigRouter(server).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/parse-node", strings.NewReader(body)))
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	}
}
