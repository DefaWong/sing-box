package fakeip

import (
	"context"
	"encoding/json"
	"net/netip"
	"testing"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"

	mDNS "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

func TestStoreResetClearsMemoryStorage(t *testing.T) {
	t.Parallel()

	store := NewStore(context.Background(), logger.NOP(), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("fc00::/18"))
	require.NoError(t, store.Start())

	address4, err := store.Create("a.example.com", false)
	require.NoError(t, err)
	address6, err := store.Create("a.example.com", true)
	require.NoError(t, err)
	_, err = store.Create("b.example.com", false)
	require.NoError(t, err)

	require.NoError(t, store.Reset())

	_, loaded := store.Lookup(address4)
	require.False(t, loaded)
	_, loaded = store.Lookup(address6)
	require.False(t, loaded)

	newAddress4, err := store.Create("c.example.com", false)
	require.NoError(t, err)
	require.Equal(t, address4, newAddress4, "allocation restarts from the beginning of the range")
	newAddress6, err := store.Create("c.example.com", true)
	require.NoError(t, err)
	require.Equal(t, address6, newAddress6)
	domain, loaded := store.Lookup(newAddress4)
	require.True(t, loaded)
	require.Equal(t, "c.example.com", domain)
}

func TestTransportExchangeFakeIPOutboundEDNS0(t *testing.T) {
	t.Parallel()

	var opts option.FakeIPDNSServerOptions
	require.NoError(t, json.Unmarshal([]byte(`{"inet4_range":"198.18.0.0/15","inet6_range":"fc00::/18"}`), &opts))
	rawTransport, err := NewTransport(context.Background(), log.NewNOPFactory().NewLogger("test"), "fakeip", opts)
	require.NoError(t, err)
	transport := rawTransport.(*Transport)
	require.NoError(t, transport.store.Start())

	makeQuery := func(qname string, qtype uint16, ednsPayload []byte) *mDNS.Msg {
		msg := new(mDNS.Msg)
		msg.SetQuestion(mDNS.Fqdn(qname), qtype)
		if ednsPayload != nil {
			opt := new(mDNS.OPT)
			opt.Hdr.Name = "."
			opt.Hdr.Rrtype = mDNS.TypeOPT
			opt.SetUDPSize(4096)
			opt.Option = append(opt.Option, &mDNS.EDNS0_LOCAL{
				Code: edns0OptionFakeIPOutbound,
				Data: ednsPayload,
			})
			msg.Extra = append(msg.Extra, opt)
		}
		return msg
	}

	// 1. Set outbound "proxy-main" on initial A query
	resp1, err := transport.Exchange(context.Background(), makeQuery("openai.com", mDNS.TypeA, append([]byte{1, 1}, []byte("proxy-main")...)))
	require.NoError(t, err)
	require.Len(t, resp1.Answer, 1)
	addr1 := netip.MustParseAddr(resp1.Answer[0].(*mDNS.A).A.String())

	outbound, loaded := transport.Store().LookupOutbound("openai.com")
	require.True(t, loaded)
	require.Equal(t, "proxy-main", outbound)

	// 2. Update outbound to "proxy-alt" on existing domain (same FakeIP returned)
	resp2, err := transport.Exchange(context.Background(), makeQuery("openai.com", mDNS.TypeA, append([]byte{1, 1}, []byte("proxy-alt")...)))
	require.NoError(t, err)
	addr2 := netip.MustParseAddr(resp2.Answer[0].(*mDNS.A).A.String())
	require.Equal(t, addr1, addr2)

	outbound, loaded = transport.Store().LookupOutbound("openai.com")
	require.True(t, loaded)
	require.Equal(t, "proxy-alt", outbound)

	// 3. Clear outbound via op=2 (CLEAR)
	_, err = transport.Exchange(context.Background(), makeQuery("openai.com", mDNS.TypeAAAA, []byte{1, 2}))
	require.NoError(t, err)

	_, loaded = transport.Store().LookupOutbound("openai.com")
	require.False(t, loaded)
}
