package main

func newTestPacketStore() *PacketStore {
	return &PacketStore{
		packets:       []*StoreTx{},
		byNode:        make(map[string][]*StoreTx),
		nodeCache:     []nodeInfo{},
		nodePM:        &prefixMap{},
		rfCache:       make(map[string]*cachedResult),
		hashCache:     make(map[string]*cachedResult),
		areaNodeCache: make(map[string]map[string]bool),
	}
}
