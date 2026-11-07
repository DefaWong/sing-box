package route

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/dns/transport/fakeip"
	"github.com/sagernet/sing-box/option"
	R "github.com/sagernet/sing-box/route/rule"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/stretchr/testify/require"
)

func TestDisabledRuleFallsThroughPreMatchAndMatch(t *testing.T) {
	router, metadata := newPreMatchQUICRouter(t, time.Minute)
	router.outbound = &testL3OutboundManager{outbounds: map[string]adapter.Outbound{}}
	disabled := &preMatchQUICRule{action: &R.RuleActionRoute{Outbound: "disabled"}, disabled: true}
	bypass := &preMatchQUICRule{action: &R.RuleActionBypass{}}
	next := &preMatchQUICRule{action: &R.RuleActionRoute{Outbound: "next"}}
	router.rules = []adapter.Rule{disabled, bypass, next}
	require.Equal(t, adapter.PreMatchBypass, router.PreMatch(metadata, nil).Action)
	matched, _, _, _, err := router.matchRule(context.Background(), &metadata, nil, nil)
	require.NoError(t, err)
	require.Same(t, next, matched)
}

func TestDisabledRuleReferenceReachability(t *testing.T) {
	runtime := []*preMatchQUICRule{{disabled: true}, {}}
	routeOptions := []option.Rule{}
	dnsOptions := []option.DNSRule{}
	for _, tag := range []string{"disabled", "fallback"} {
		routeOptions = append(routeOptions, option.Rule{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultRule{RuleAction: option.RuleAction{Action: C.RuleActionTypeRoute, RouteOptions: option.RouteActionOptions{Outbound: tag}}}})
		dnsOptions = append(dnsOptions, option.DNSRule{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultDNSRule{DNSRuleAction: option.DNSRuleAction{Action: C.RuleActionTypeRoute, RouteOptions: option.DNSRouteActionOptions{Server: tag}}}})
	}
	manager := &testL3OutboundManager{outbounds: map[string]adapter.Outbound{}}
	var outbounds, transports []string
	require.True(t, collectRuleReferences(enabledRuleOptions(routeOptions, runtime), "", manager, &outbounds, &transports))
	require.Equal(t, []string{"fallback"}, outbounds)
	require.True(t, collectDNSRuleReferences(enabledRuleOptions(dnsOptions, runtime), "", &transports))
	require.Equal(t, []string{"fallback"}, transports)
	runtime[1].disabled = true
	require.False(t, collectRuleReferences(enabledRuleOptions(routeOptions, runtime), "", manager, &outbounds, &transports))
	require.False(t, collectDNSRuleReferences(enabledRuleOptions(dnsOptions, runtime), "", &transports))
	runtime[0].disabled = false
	outbounds = nil
	require.True(t, collectRuleReferences(enabledRuleOptions(routeOptions, runtime), "", manager, &outbounds, &transports))
	require.Equal(t, []string{"disabled"}, outbounds)
}

type testFakeIPTransport struct {
	adapter.FakeIPTransport
	store adapter.FakeIPStore
}

func (t *testFakeIPTransport) Store() adapter.FakeIPStore {
	return t.store
}

type testFakeIPDNSTransportManager struct {
	adapter.DNSTransportManager
	fakeIP adapter.FakeIPTransport
}

func (m *testFakeIPDNSTransportManager) FakeIP() adapter.FakeIPTransport {
	return m.fakeIP
}

type testCapturingOutbound struct {
	testFlowOutbound
	routedMetadata *adapter.InboundContext
}

func (o *testCapturingOutbound) NewConnection(_ context.Context, conn net.Conn, metadata adapter.InboundContext, onClose func(error)) {
	o.routedMetadata = &metadata
	_ = conn.Close()
	if onClose != nil {
		onClose(nil)
	}
}

func TestFakeIPOutboundDirectRouteAndStrictMissingRejection(t *testing.T) {
	store := fakeip.NewStore(context.Background(), logger.NOP(), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("fc00::/18"))
	require.NoError(t, store.Start())

	fakeAddr, err := store.CreateWithOutbound("openai.com", false, "proxy-alt", true)
	require.NoError(t, err)

	defaultOut := &testCapturingOutbound{testFlowOutbound: testFlowOutbound{outboundType: "socks", tag: "proxy-main"}}
	altOut := &testCapturingOutbound{testFlowOutbound: testFlowOutbound{outboundType: "socks", tag: "proxy-alt"}}

	router, _ := newPreMatchQUICRouter(t, time.Minute)
	router.dnsTransport = &testFakeIPDNSTransportManager{
		fakeIP: &testFakeIPTransport{store: store},
	}
	router.outbound = &testL3OutboundManager{
		defaultOutbound: defaultOut,
		outbounds: map[string]adapter.Outbound{
			"proxy-main": defaultOut,
			"proxy-alt":  altOut,
		},
	}
	// Conflicting route rule that would normally send everything to proxy-main
	router.rules = []adapter.Rule{
		&preMatchQUICRule{action: &R.RuleActionRoute{Outbound: "proxy-main"}},
	}

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	meta := adapter.InboundContext{
		Source:      M.ParseSocksaddr("10.0.0.16:50000"),
		Destination: M.SocksaddrFrom(fakeAddr, 443),
	}
	require.NoError(t, router.routeConnection(context.Background(), c1, meta, nil))
	require.NotNil(t, altOut.routedMetadata, "connection must route directly to proxy-alt")
	require.Nil(t, defaultOut.routedMetadata, "conflicting route.rules must be bypassed")
	require.Equal(t, "openai.com", altOut.routedMetadata.Destination.Fqdn)
	require.Equal(t, "proxy-alt", altOut.routedMetadata.RouteOutbound)

	// Now bind a non-existent outbound and verify strict rejection
	_, err = store.CreateWithOutbound("openai.com", false, "deleted-outbound", true)
	require.NoError(t, err)

	c3, c4 := net.Pipe()
	defer c3.Close()
	defer c4.Close()
	err = router.routeConnection(context.Background(), c3, meta, nil)
	require.ErrorContains(t, err, "outbound not found: deleted-outbound")

	preMatchRes := router.PreMatch(meta, nil)
	require.Equal(t, adapter.PreMatchReject, preMatchRes.Action)
}
