package fakeip

import (
	"net/netip"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/logger"
)

var _ adapter.FakeIPStorage = (*MemoryStorage)(nil)

type MemoryStorage struct {
	addressAccess sync.RWMutex
	domainAccess  sync.RWMutex
	addressCache  map[netip.Addr]string
	domainCache4  map[string]netip.Addr
	domainCache6  map[string]netip.Addr
	outboundCache map[string]string
}

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{
		addressCache:  make(map[netip.Addr]string),
		domainCache4:  make(map[string]netip.Addr),
		domainCache6:  make(map[string]netip.Addr),
		outboundCache: make(map[string]string),
	}
}

func (s *MemoryStorage) FakeIPMetadata() *adapter.FakeIPMetadata {
	return nil
}

func (s *MemoryStorage) FakeIPSaveMetadata(metadata *adapter.FakeIPMetadata) error {
	return nil
}

func (s *MemoryStorage) FakeIPSaveMetadataAsync(metadata *adapter.FakeIPMetadata) {
}

func (s *MemoryStorage) FakeIPStore(address netip.Addr, domain string) error {
	s.addressAccess.Lock()
	s.domainAccess.Lock()
	if oldDomain, loaded := s.addressCache[address]; loaded {
		if address.Is4() {
			delete(s.domainCache4, oldDomain)
		} else {
			delete(s.domainCache6, oldDomain)
		}
	}
	s.addressCache[address] = domain
	if address.Is4() {
		s.domainCache4[domain] = address
	} else {
		s.domainCache6[domain] = address
	}
	s.domainAccess.Unlock()
	s.addressAccess.Unlock()
	return nil
}

func (s *MemoryStorage) FakeIPStoreAsync(address netip.Addr, domain string, logger logger.Logger) {
	_ = s.FakeIPStore(address, domain)
}

func (s *MemoryStorage) FakeIPLoad(address netip.Addr) (string, bool) {
	s.addressAccess.RLock()
	defer s.addressAccess.RUnlock()
	domain, loaded := s.addressCache[address]
	return domain, loaded
}

func (s *MemoryStorage) FakeIPLoadDomain(domain string, isIPv6 bool) (netip.Addr, bool) {
	s.domainAccess.RLock()
	defer s.domainAccess.RUnlock()
	if !isIPv6 {
		address, loaded := s.domainCache4[domain]
		return address, loaded
	} else {
		address, loaded := s.domainCache6[domain]
		return address, loaded
	}
}

func (s *MemoryStorage) FakeIPStoreOutbound(domain string, outbound string) error {
	s.domainAccess.Lock()
	if outbound == "" {
		delete(s.outboundCache, domain)
	} else {
		s.outboundCache[domain] = outbound
	}
	s.domainAccess.Unlock()
	return nil
}

func (s *MemoryStorage) FakeIPLoadOutbound(domain string) (string, bool) {
	s.domainAccess.RLock()
	defer s.domainAccess.RUnlock()
	outbound, loaded := s.outboundCache[domain]
	return outbound, loaded && outbound != ""
}

func (s *MemoryStorage) FakeIPReset() error {
	s.addressAccess.Lock()
	s.domainAccess.Lock()
	s.addressCache = make(map[netip.Addr]string)
	s.domainCache4 = make(map[string]netip.Addr)
	s.domainCache6 = make(map[string]netip.Addr)
	s.outboundCache = make(map[string]string)
	s.domainAccess.Unlock()
	s.addressAccess.Unlock()
	return nil
}
