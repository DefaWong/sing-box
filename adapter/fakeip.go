package adapter

import (
	"net/netip"

	"github.com/sagernet/sing/common/logger"
)

type FakeIPStore interface {
	SimpleLifecycle
	Contains(address netip.Addr) bool
	Create(domain string, isIPv6 bool) (netip.Addr, error)
	CreateWithOutbound(domain string, isIPv6 bool, outbound string, updateOutbound bool) (netip.Addr, error)
	Lookup(address netip.Addr) (string, bool)
	LookupOutbound(domain string) (string, bool)
	Reset() error
}

type FakeIPStorage interface {
	FakeIPMetadata() *FakeIPMetadata
	FakeIPSaveMetadata(metadata *FakeIPMetadata) error
	FakeIPSaveMetadataAsync(metadata *FakeIPMetadata)
	FakeIPStore(address netip.Addr, domain string) error
	FakeIPStoreAsync(address netip.Addr, domain string, logger logger.Logger)
	FakeIPLoad(address netip.Addr) (string, bool)
	FakeIPLoadDomain(domain string, isIPv6 bool) (netip.Addr, bool)
	FakeIPStoreOutbound(domain string, outbound string) error
	FakeIPLoadOutbound(domain string) (string, bool)
	FakeIPReset() error
}

type FakeIPTransport interface {
	DNSTransport
	Store() FakeIPStore
}
