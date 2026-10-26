package main

import (
	"fmt"
	"testing"
	"time"
)

// Exercise the real reader and batch path-update entry points with 10,000
// transmissions, 30,000 hop records and 10,000 path records. Setup is excluded.
func BenchmarkDistanceOwnership(b *testing.B) {
	for _, operation := range []string{"analytics-read", "path-update"} {
		b.Run(operation, func(b *testing.B) {
			s := NewPacketStore(nil, nil)
			nodes := []nodeInfo{
				{PublicKey: "aa", Name: "A", Role: "repeater", HasGPS: true, Lat: 1, Lon: 1},
				{PublicKey: "bb", Name: "B", Role: "repeater", HasGPS: true, Lat: 1, Lon: 1.01},
				{PublicKey: "cc", Name: "C", Role: "repeater", HasGPS: true, Lat: 1, Lon: 1.02},
				{PublicKey: "dd", Name: "D", Role: "repeater", HasGPS: true, Lat: 1, Lon: 1.03},
			}
			s.nodeCache, s.nodePM, s.nodeCacheTime = nodes, buildPrefixMap(nodes), time.Now()
			const count = 10000
			s.distHops = make([]distHopRecord, 0, count*3)
			s.distPaths = make([]distPathRecord, 0, count)
			stamp := time.Now().UTC().Format(time.RFC3339)
			for i := 0; i < count; i++ {
				tx := &StoreTx{ID: i + 1, Hash: fmt.Sprintf("synthetic-%d", i), FirstSeen: stamp,
					PathJSON: `["aa","bb","cc","dd"]`, parsedPath: []string{"aa", "bb", "cc", "dd"}, pathParsed: true}
				s.packets = append(s.packets, tx)
				details := make([]distHopDetail, 3)
				for j := range details {
					a, z := nodes[j], nodes[j+1]
					s.distHops = append(s.distHops, distHopRecord{FromName: a.Name, FromPk: a.PublicKey,
						ToName: z.Name, ToPk: z.PublicKey, Dist: 1, Type: "R↔R", Hash: tx.Hash, Timestamp: stamp, tx: tx})
					details[j] = distHopDetail{FromName: a.Name, FromPk: a.PublicKey, ToName: z.Name, ToPk: z.PublicKey, Dist: 1}
				}
				s.distPaths = append(s.distPaths, distPathRecord{Hash: tx.Hash, TotalDist: 3, HopCount: 3,
					Timestamp: stamp, Hops: details, tx: tx})
			}
			changed := s.packets[:1]
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if operation == "analytics-read" {
					s.computeAnalyticsDistance("", "")
				} else {
					s.mu.Lock()
					s.updateDistanceIndexForTxs(changed)
					s.mu.Unlock()
				}
			}
		})
	}
}
