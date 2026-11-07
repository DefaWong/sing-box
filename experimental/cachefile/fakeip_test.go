package cachefile

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFakeIPResetWithMissingBuckets(t *testing.T) {
	t.Parallel()

	cache := newDNSCacheTestCache(t)
	require.NoError(t, cache.FakeIPReset())

	address := netip.MustParseAddr("198.18.0.2")
	require.NoError(t, cache.FakeIPStore(address, "a.example.com"))
	require.NoError(t, cache.FakeIPReset())
	_, loaded := cache.FakeIPLoad(address)
	require.False(t, loaded)
	_, loaded = cache.FakeIPLoadDomain("a.example.com", false)
	require.False(t, loaded)
}

func TestFakeIPOutboundPersistenceAndClear(t *testing.T) {
	t.Parallel()

	cache := newDNSCacheTestCache(t)
	require.NoError(t, cache.FakeIPStoreOutbound("openai.com", "proxy-main"))
	outbound, loaded := cache.FakeIPLoadOutbound("openai.com")
	require.True(t, loaded)
	require.Equal(t, "proxy-main", outbound)

	require.NoError(t, cache.FakeIPStoreOutbound("openai.com", "proxy-alt"))
	outbound, loaded = cache.FakeIPLoadOutbound("openai.com")
	require.True(t, loaded)
	require.Equal(t, "proxy-alt", outbound)

	require.NoError(t, cache.FakeIPStoreOutbound("openai.com", ""))
	_, loaded = cache.FakeIPLoadOutbound("openai.com")
	require.False(t, loaded)
}
