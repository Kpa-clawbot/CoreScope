package main

import (
	"fmt"
	"log"
)

func (c *PlannerConfig) enabled() bool { return c != nil && c.Enabled }

// resolved applies defaults; invalid values fall back with a warning.
func (c *PlannerConfig) resolved() (PlannerConfig, []string) {
	out := PlannerConfig{
		DefaultRangeKm:   defaultPlannerRangeKm,
		NewAntennaM:      defaultPlannerNewAntennaM,
		ExistingAntennaM: defaultPlannerExistAntenna,
		MaxAreaKm:        defaultPlannerMaxAreaKm,
		NodeMarginKm:     defaultPlannerNodeMarginKm,
		MaxLegKm:         defaultPlannerMaxLegKm,
	}
	if c == nil {
		return out, nil
	}
	var warns []string
	out.Enabled = c.Enabled
	if c.DefaultRangeKm != 0 {
		if plannerRangeAllowed(c.DefaultRangeKm) {
			out.DefaultRangeKm = c.DefaultRangeKm
		} else {
			warns = append(warns, "planner.defaultRangeKm must be one of 10,15,20,30,40,50,60; using the default")
		}
	}
	if c.NewAntennaM != 0 {
		if c.NewAntennaM > 0 && c.NewAntennaM <= 100 {
			out.NewAntennaM = c.NewAntennaM
		} else {
			warns = append(warns, "planner.newAntennaM must be within 0-100; using the default")
		}
	}
	if c.ExistingAntennaM != 0 {
		if c.ExistingAntennaM > 0 && c.ExistingAntennaM <= 100 {
			out.ExistingAntennaM = c.ExistingAntennaM
		} else {
			warns = append(warns, "planner.existingAntennaM must be within 0-100; using the default")
		}
	}
	out.MaxAreaKm = plannerSizeKm("maxAreaKm", c.MaxAreaKm, defaultPlannerMaxAreaKm, plannerAreaKmMin, plannerAreaKmMax, &warns)
	out.NodeMarginKm = plannerSizeKm("nodeMarginKm", c.NodeMarginKm, defaultPlannerNodeMarginKm,
		plannerMarginKmMin, plannerMarginKmMax, &warns)
	out.MaxLegKm = plannerSizeKm("maxLegKm", c.MaxLegKm, defaultPlannerMaxLegKm, plannerLegKmMin, plannerLegKmMax, &warns)
	// No range above the leg limit is offered, so the default cannot be one.
	if out.DefaultRangeKm > out.MaxLegKm {
		r := out.rangesKm()
		warns = append(warns, fmt.Sprintf("planner.defaultRangeKm %g is over planner.maxLegKm %g; using %g",
			out.DefaultRangeKm, out.MaxLegKm, r[len(r)-1]))
		out.DefaultRangeKm = r[len(r)-1]
	}
	return out, warns
}

// plannerSizeKm resolves one size limit: v when it lies within lo-hi, def
// when it is unset (0), and def with a warning otherwise (NaN included).
func plannerSizeKm(name string, v, def, lo, hi float64, warns *[]string) float64 {
	switch {
	case v == 0:
		return def
	case v >= lo && v <= hi:
		return v
	}
	*warns = append(*warns, fmt.Sprintf("planner.%s must be within %g-%g; using the default", name, lo, hi))
	return def
}

// rangesKm is the range values resolved c accepts and offers: those of
// plannerRangesKm up to MaxLegKm, since a relay leg is never longer than the
// A→B leg. The smallest range equals the smallest MaxLegKm, so it is never
// empty.
func (c PlannerConfig) rangesKm() []float64 {
	n := 0
	for n < len(plannerRangesKm) && plannerRangesKm[n] <= c.MaxLegKm {
		n++
	}
	return plannerRangesKm[:n:n]
}

// tileLimits is the per-endpoint terrain tile caps for c's size limits. A
// size that is 0 counts as its default, as in resolved().
func (c PlannerConfig) tileLimits() plannerTileLimits {
	or := func(v, def float64) float64 {
		if v == 0 {
			return def
		}
		return v
	}
	return plannerTileCaps(or(c.MaxAreaKm, defaultPlannerMaxAreaKm), or(c.NodeMarginKm, defaultPlannerNodeMarginKm),
		or(c.MaxLegKm, defaultPlannerMaxLegKm))
}

func plannerRangeAllowed(km float64) bool {
	for _, r := range plannerRangesKm {
		if r == km {
			return true
		}
	}
	return false
}

// clientConfig is /api/config/client "planner"; nil when disabled. Radio
// defaults come from the los block (frequency, k) so both tools agree.
func (c *PlannerConfig) clientConfig(los *LOSConfig) *PlannerClientConfig {
	if !c.enabled() {
		return nil
	}
	r, _ := c.resolved()
	l, _ := los.resolved()
	return &PlannerClientConfig{
		DefaultRangeKm:   r.DefaultRangeKm,
		RangesKm:         r.rangesKm(),
		NewAntennaM:      r.NewAntennaM,
		ExistingAntennaM: r.ExistingAntennaM,
		FrequencyMHz:     l.FrequencyMHz,
		KFactor:          l.KFactor,
		MaxPoints:        plannerMaxPoints,
		MaxLegKm:         r.MaxLegKm,
		MaxAreaKm:        r.MaxAreaKm,
		NodeMarginKm:     r.NodeMarginKm,
		MaxSites:         plannerMaxSites,
		MinClearance:     plannerMinClearance,
	}
}

func logPlannerConfig(c *PlannerConfig) {
	if !c.enabled() {
		return
	}
	r, warns := c.resolved()
	for _, w := range warns {
		log.Printf("[planner] %s", w)
	}
	lim := r.tileLimits()
	log.Printf("[planner] enabled (range=%.0f km, new=%.0f m, existing=%.0f m, "+
		"maxArea=%g km, nodeMargin=%g km, maxLeg=%g km)",
		r.DefaultRangeKm, r.NewAntennaM, r.ExistingAntennaM, r.MaxAreaKm, r.NodeMarginKm, r.MaxLegKm)
	log.Printf("[planner] terrain tiles per request: coverage %d, relay %d, profile %d; "+
		"one view takes up to %.0f MB (%.1f MB a tile)",
		lim.Coverage, lim.Relay, lim.Profile, float64(lim.max())*srtmTileBytes/1e6, float64(srtmTileBytes)/1e6)
}
