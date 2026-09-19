package main

import "testing"

// clientRegionsMsg builds a valid region-discovery answer on the dedicated
// topic meshcore/client/<pubkey>/regions.
func clientRegionsMsg() *mockMessage {
	payload := []byte(`{"type":"REGIONS","timestamp":"2026-06-09T12:00:00Z","target":"` + testTargetPK + `","regions":["be","be-vlg"],"truncated":false,"gps":{"lat":51.05,"lon":3.72,"acc_m":8.0}}`)
	return &mockMessage{topic: "meshcore/client/" + testCompanionPK + "/regions", payload: payload}
}

// clientRegionsMsgWithRaw is clientRegionsMsg plus a "raw" field (same advert
// hex as clientCoverageMsg). The real app never sends "raw" on /regions, but
// the fall-through guard must be structural — independent of payload shape —
// so this proves the dispatch itself (not payload luck) is what keeps a
// /regions message with an incidental "raw" field from being decoded as an
// ordinary observer packet were the gate ever to fall through.
func clientRegionsMsgWithRaw() *mockMessage {
	advertHex := "11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172"
	payload := []byte(`{"raw":"` + advertHex + `","direction":"rx","type":"REGIONS","timestamp":"2026-06-09T12:00:00Z","origin":"MyMob","target":"` + testTargetPK + `","regions":["be"],"truncated":false,"gps":{"lat":51.05,"lon":3.72,"acc_m":8.0}}`)
	return &mockMessage{topic: "meshcore/client/" + testCompanionPK + "/regions", payload: payload}
}

// TestClientRegionsEnabledDefault verifies the gate helper defaults OFF for
// nil/absent config and is only true when explicitly enabled.
func TestClientRegionsEnabledDefault(t *testing.T) {
	if (&Config{}).ClientRegionsEnabled() {
		t.Fatal("nil ClientRegions must report disabled")
	}
	if (&Config{ClientRegions: &ClientRegionsConfig{Enabled: false}}).ClientRegionsEnabled() {
		t.Fatal("Enabled:false must report disabled")
	}
	if !(&Config{ClientRegions: &ClientRegionsConfig{Enabled: true}}).ClientRegionsEnabled() {
		t.Fatal("Enabled:true must report enabled")
	}
}

// TestClientRegionsGateOn drives handleMessage with the feature ON: the
// /regions topic message must be dispatched and write exactly one row.
func TestClientRegionsGateOn(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}
	cfg := &Config{ClientRegions: &ClientRegionsConfig{Enabled: true}}

	handleMessage(store, "test", source, clientRegionsMsg(), nil, nil, cfg)

	if n := declaredRegionsCount(t, store); n != 1 {
		t.Fatalf("feature ON: expected 1 node_declared_regions row, got %d", n)
	}
}

// TestClientRegionsGateOffDoesNotFallThroughToObserverPath is the
// dispatch-shape regression test for the /regions topic, mirroring
// TestClientRfSamplesGateOffDoesNotFallThroughToObserverPath. With the
// feature OFF, a meshcore/client/<pubkey>/regions message must be dropped
// outright — never fall through to the observer packet path, which would
// take parts[1] ("client") as a region and parts[2] (the companion pubkey)
// as an observer id.
func TestClientRegionsGateOffDoesNotFallThroughToObserverPath(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}
	cfg := &Config{} // ClientRegions nil ⇒ disabled

	handleMessage(store, "test", source, clientRegionsMsgWithRaw(), nil, nil, cfg)

	if n := declaredRegionsCount(t, store); n != 0 {
		t.Fatalf("feature OFF: expected 0 node_declared_regions rows, got %d", n)
	}
	var observerRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM observers WHERE id = ?`, testCompanionPK).Scan(&observerRows); err != nil {
		t.Fatal(err)
	}
	if observerRows != 0 {
		t.Fatalf("feature OFF: the companion pubkey must not be registered as an observer, got %d rows", observerRows)
	}
	var clientRegionRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM observers WHERE iata = 'client'`).Scan(&clientRegionRows); err != nil {
		t.Fatal(err)
	}
	if clientRegionRows != 0 {
		t.Fatalf("feature OFF: no observer should be registered under a bogus 'client' region, got %d rows", clientRegionRows)
	}
	var txRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transmissions`).Scan(&txRows); err != nil {
		t.Fatal(err)
	}
	if txRows != 0 {
		t.Fatalf("feature OFF: the client-topic message must not be ingested as an ordinary observer packet, got %d transmissions rows", txRows)
	}
}

// TestClientRegionsBlacklistedDropped verifies a blacklisted operator cannot
// skirt the observer blacklist via the /regions topic, mirroring
// TestClientRfSamplesBlacklistedDropped.
func TestClientRegionsBlacklistedDropped(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}
	cfg := &Config{
		ClientRegions:     &ClientRegionsConfig{Enabled: true},
		ObserverBlacklist: []string{testCompanionPK},
	}

	handleMessage(store, "test", source, clientRegionsMsg(), nil, nil, cfg)

	if n := declaredRegionsCount(t, store); n != 0 {
		t.Fatalf("blacklisted companion: expected 0 node_declared_regions rows, got %d", n)
	}
}

// TestClientOtherGatesUnaffectedByRegionsGate verifies the client sub-topic
// gates are independent: /regions disabled must not block /packets when
// clientRxCoverage is enabled.
func TestClientOtherGatesUnaffectedByRegionsGate(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}
	cfg := &Config{
		ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true},
		ClientRegions:    &ClientRegionsConfig{Enabled: false},
	}

	handleMessage(store, "test", source, clientCoverageMsg(), nil, nil, cfg)

	if n := clientReceptionCount(t, store); n != 1 {
		t.Fatalf("expected 1 client_receptions row with clientRxCoverage on / clientRegions off, got %d", n)
	}
}
