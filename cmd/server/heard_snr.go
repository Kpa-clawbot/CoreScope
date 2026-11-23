package main

import (
	"math"
	"time"
)

// SNR detail kept per (node, observer) by the direct-heard pass, so the node
// analytics page can show median, spread and change over time of what an
// observer received from the node itself, without a per-request scan.
//
// SNR is binned at 0.25 dB: the firmware reports SNR as a signed byte in
// quarter dB, so the bins lose nothing.

// heardRecentMax bounds the ring of newest receptions per (node, observer)
// that feeds snrTrend.
const heardRecentMax = 500

// snrBin is one non-empty histogram bin: Bin is the quarter-dB value offset
// by 128, N the number of receptions in it.
type snrBin struct {
	Bin uint8
	N   uint32
}

// heardHour holds one hour of direct receptions: N counts all of them, Bins
// only those with an SNR. Sparse because an hour has few distinct values.
type heardHour struct {
	N    uint32
	Bins []snrBin
}

// heardSample is one reception in the ring: unix seconds, SNR in quarter dB
// (math.MinInt8 when absent), RSSI in dBm (math.MinInt16 when absent).
type heardSample struct {
	T    uint32
	SNR  int8
	RSSI int16
}

// snrQuarterBin rounds snr to 0.25 dB, clamps it to the signed-byte range
// and offsets it by 128.
func snrQuarterBin(snr float64) uint8 {
	q := math.Round(snr * 4)
	q = math.Max(-128, math.Min(127, q))
	return uint8(int(q) + 128)
}

// binSNR is the dB value of a bin.
func binSNR(b uint8) float64 {
	return float64(int(b)-128) / 4
}

// addSNR counts one SNR value in the hour's histogram.
func (h *heardHour) addSNR(snr float64) {
	b := snrQuarterBin(snr)
	for i := range h.Bins {
		if h.Bins[i].Bin == b {
			h.Bins[i].N++
			return
		}
	}
	h.Bins = append(h.Bins, snrBin{Bin: b, N: 1})
}

// newHeardSample packs one reception for the ring.
func newHeardSample(t time.Time, snr, rssi *float64) heardSample {
	s := heardSample{T: uint32(t.Unix()), SNR: math.MinInt8, RSSI: math.MinInt16}
	if snr != nil {
		// -128 is the "absent" marker, so a real value clamps to -127.
		s.SNR = int8(math.Max(-127, math.Min(127, math.Round(*snr*4))))
	}
	if rssi != nil {
		s.RSSI = int16(math.Max(-32767, math.Min(32767, math.Round(*rssi))))
	}
	return s
}

// snrHist merges hourly histograms for one window.
type snrHist struct {
	counts [256]uint32
	total  uint32
}

func (h *snrHist) addHour(x *heardHour) {
	for _, b := range x.Bins {
		h.counts[b.Bin] += b.N
		h.total += b.N
	}
}

// quantile returns the nearest-rank q-quantile, or ok == false when the
// histogram is empty.
func (h *snrHist) quantile(q float64) (float64, bool) {
	if h.total == 0 {
		return 0, false
	}
	rank := uint32(math.Ceil(q * float64(h.total)))
	if rank < 1 {
		rank = 1
	}
	var seen uint32
	for b, n := range h.counts {
		seen += n
		if seen >= rank {
			return binSNR(uint8(b)), true
		}
	}
	return binSNR(255), true
}
