package packetpath

// Route type constants (header bits 1-0).
const (
	RouteTransportFlood  = 0
	RouteFlood           = 1
	RouteDirect          = 2
	RouteTransportDirect = 3
)

// PayloadTRACE is the payload type constant for TRACE packets.
const PayloadTRACE = 0x09

// IsTransportRoute returns true for TRANSPORT_FLOOD (0) and TRANSPORT_DIRECT (3).
func IsTransportRoute(routeType int) bool {
	return routeType == RouteTransportFlood || routeType == RouteTransportDirect
}

// PathBytesAreHops returns true when the raw_hex header path bytes represent
// route hop hashes (the normal case). Returns false for packet types where
// header path bytes are repurposed (e.g. TRACE uses them for SNR values).
func PathBytesAreHops(payloadType byte) bool {
	return payloadType != PayloadTRACE
}

// Payload types the route predicates below single out.
const (
	PayloadADVERT   = 0x04
	PayloadANON_REQ = 0x07
)

// PathHopsTransmitted reports whether a path lists relays that already
// transmitted the packet, so its last element is the node the observer heard.
// Only a flood route builds its path that way: each forwarder appends its own
// hash before retransmitting (firmware Mesh.cpp). A direct-route path is the
// REMAINING route (Mesh::removeSelfFromPath), and TRACE path bytes are per-hop
// SNR values. An unknown route type proves nothing.
func PathHopsTransmitted(routeType int, payloadType byte) bool {
	if !PathBytesAreHops(payloadType) {
		return false
	}
	return routeType == RouteFlood || routeType == RouteTransportFlood
}

// EmptyPathHeardOriginator reports whether an observation with an empty path
// was received from the originator's own transmission. On a flood route an
// empty path means no forwarder appended itself. On a direct route an empty
// path is also what the last forwarder of a sendDirect leaves after
// removeSelfFromPath, so only an ADVERT counts there: adverts go out by flood
// or by Mesh::sendZeroHop, never routed.
func EmptyPathHeardOriginator(routeType int, payloadType byte) bool {
	if !PathBytesAreHops(payloadType) {
		return false
	}
	switch routeType {
	case RouteFlood, RouteTransportFlood:
		return true
	case RouteDirect, RouteTransportDirect:
		return payloadType == PayloadADVERT
	}
	return false
}
