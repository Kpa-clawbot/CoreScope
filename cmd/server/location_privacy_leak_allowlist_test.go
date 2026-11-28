package main

// knownLocationLeaks is the ratchet of the location-privacy leak test
// (location_privacy_leak_test.go): every "<route> <check>" pair that reveals
// an exact position today, with the ticket that closes it.
//
//	B3 coordinates    node positions (nodes, inactive_nodes) and what is
//	                  computed from them: distances, planner, reach, route LOS.
//	B4 companion GPS  client_receptions / client_rf_samples and what is
//	                  derived from them: coverage cells, RF noise, estimates.
//	B5 packets        located adverts: raw_hex, decoded_json, the live feed.
//
// "B3 coordinates, B5 packets": the route serves node rows and adverts, and
// the entry goes only when both are done.
//
// The test fails on a leak that is not listed here, and on an entry that no
// longer leaks: a ticket deletes its entries as it closes them. Never add one
// to get a route through. The finished feature has an empty map.
//
// Routes are GET templates; "POST …" are the data POSTs, "ws:…" the live
// feed, "oracle:…" the oracle checks. Regenerate with
//
//	LOCPRIV_LEAK_REPORT=1 go test -run LocationLeak ./...
//
// which prints the current set in this format.
//
// Fixture gaps: none. Every route answers 200 from the seeded data except
// GET /api/notifications/unsubscribe (303, a redirect by design).
var knownLocationLeaks = map[string]string{}
