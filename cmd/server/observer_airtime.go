package main

import (
	"math"
	"time"
)

// Rolling 1 h airtime per observer (#661). The firmware enforces its own
// duty-cycle budget over a 1 h window (Dispatcher.cpp, duty_cycle_window_ms),
// but repeaters default to airtime_factor 1.0, i.e. a 50% budget, so the EU
// 10% limit is not enforced on the device. The summary therefore reports the
// TX share of the last hour and the threshold the UI warns at.

// airtimeWindow is the span the rolling figure covers, matching the
// firmware's duty-cycle window.
const airtimeWindow = time.Hour

// airtimeMinSpan is the shortest stretch of samples that yields a figure;
// shorter (one sample, or a reboot late in the hour) reads as unknown. The
// ingestor rounds sample times to its sample interval (5 min by default), so
// the span can be off by up to one interval: about 17% at 30 min, 8% at 1 h.
const airtimeMinSpan = 30 * time.Minute

// airtimeMaxAge is how old the newest sample may be; an observer that has
// gone quiet longer than this reads as unknown rather than "last hour".
const airtimeMaxAge = 15 * time.Minute

// defaultDutyCycleWarnPct is the EU 868 MHz g3 sub-band duty-cycle limit.
const defaultDutyCycleWarnPct = 10.0

// airtimePoint is one sample of a cumulative airtime counter (seconds since
// boot); Secs is nil when the observer did not report it.
type airtimePoint struct {
	At   time.Time
	Secs *int
}

// rollingAirtimePct returns the airtime share, in percent, over the points
// given in time order. A counter that goes down marks a reboot: only the
// stretch after the last reset counts. Returns nil when that stretch is
// shorter than airtimeMinSpan, or the newest sample is older than
// airtimeMaxAge at now.
func rollingAirtimePct(pts []airtimePoint, now time.Time) *float64 {
	var start, last *airtimePoint
	for i := range pts {
		p := &pts[i]
		if p.Secs == nil {
			continue
		}
		if start == nil || *p.Secs < *last.Secs {
			start = p
		}
		last = p
	}
	if start == nil || now.Sub(last.At) > airtimeMaxAge {
		return nil
	}
	span := last.At.Sub(start.At)
	if span < airtimeMinSpan {
		return nil
	}
	pct := float64(*last.Secs-*start.Secs) / span.Seconds() * 100
	pct = math.Min(math.Max(pct, 0), 100)
	pct = math.Round(pct*100) / 100
	return &pct
}

// DutyCycleWarnPctOrDefault returns the configured duty-cycle warning
// threshold in percent, or the EU 10% default.
func (c *Config) DutyCycleWarnPctOrDefault() float64 {
	if c.DutyCycleWarnPct > 0 {
		return c.DutyCycleWarnPct
	}
	return defaultDutyCycleWarnPct
}

// MetricsSummaryResponse is the body of GET /api/observers/metrics/summary.
type MetricsSummaryResponse struct {
	Observers        []MetricsSummaryRow `json:"observers"`
	DutyCycleWarnPct float64             `json:"duty_cycle_warn_pct"`
}
