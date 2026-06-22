// Package lora implements closed-form LoRa Time-on-Air calculations.
//
// Issue #1768 — see toa.go for the final implementation. This file
// holds the public API stub used by the failing red commit; the green
// commit replaces TimeOnAir's body with the closed-form expression.
package lora

import "time"

// Preset captures the LoRa PHY parameters needed to compute ToA.
type Preset struct {
	FreqHz   float64
	BWkHz    float64
	SF       int
	CR       int
	Preamble int
}

// PreambleForSF returns MeshCore's SF-dependent preamble length.
// Stub: returns 0 in the red commit.
func PreambleForSF(sf int) int { return 0 }

// TimeOnAir returns the LoRa time-on-air for a payload.
// Stub: returns 0 in the red commit.
func TimeOnAir(payloadBytes int, preset Preset) time.Duration { return 0 }
