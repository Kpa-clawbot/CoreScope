package users

import (
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	RepeaterStatusActive         = "active"
	RepeaterStatusMaintenance    = "maintenance"
	RepeaterStatusDecommissioned = "decommissioned"

	RepeaterPowerMains   = "mains"
	RepeaterPowerBattery = "battery"
	RepeaterPowerSolar   = "solar"

	MaxRepeaterNotes   = 500
	MaxRepeaterHeightM = 300
	MinRepeaterGainDBi = -10
	MaxRepeaterGainDBi = 30
)

// RepeaterEdit is one community edit of a repeater's details. HeightM nil is
// unknown (distinct from 0); an empty Status or Power is unset.
type RepeaterEdit struct {
	HeightM *float64
	GainDBi float64
	Status  string
	Power   string
	Notes   string
}

// ValidateRepeaterEdit returns the cleaned edit or a *ValidationError.
func ValidateRepeaterEdit(e RepeaterEdit) (RepeaterEdit, error) {
	if e.HeightM != nil {
		h := *e.HeightM
		if math.IsNaN(h) || math.IsInf(h, 0) || h < 0 || h > MaxRepeaterHeightM {
			return RepeaterEdit{}, &ValidationError{Msg: fmt.Sprintf("height must be between 0 and %d m", MaxRepeaterHeightM)}
		}
		hc := h
		e.HeightM = &hc
	}
	if math.IsNaN(e.GainDBi) || math.IsInf(e.GainDBi, 0) || e.GainDBi < MinRepeaterGainDBi || e.GainDBi > MaxRepeaterGainDBi {
		return RepeaterEdit{}, &ValidationError{Msg: fmt.Sprintf("antenna gain must be between %d and %d dBi", MinRepeaterGainDBi, MaxRepeaterGainDBi)}
	}
	switch e.Status {
	case "", RepeaterStatusActive, RepeaterStatusMaintenance, RepeaterStatusDecommissioned:
	default:
		return RepeaterEdit{}, &ValidationError{Msg: "status must be active, maintenance or decommissioned"}
	}
	switch e.Power {
	case "", RepeaterPowerMains, RepeaterPowerBattery, RepeaterPowerSolar:
	default:
		return RepeaterEdit{}, &ValidationError{Msg: "power must be mains, battery or solar"}
	}
	n := strings.ToValidUTF8(e.Notes, "")
	n = strings.ReplaceAll(n, "\r\n", "\n")
	n = strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\r':
			return ' '
		case r != '\n' && unicode.IsControl(r):
			return -1
		case (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069):
			return -1
		}
		return r
	}, n)
	n = strings.TrimSpace(n)
	for strings.Contains(n, "\n\n\n") {
		n = strings.ReplaceAll(n, "\n\n\n", "\n\n")
	}
	if utf8.RuneCountInString(n) > MaxRepeaterNotes {
		return RepeaterEdit{}, &ValidationError{Msg: fmt.Sprintf("notes must be at most %d characters", MaxRepeaterNotes)}
	}
	e.Notes = n
	return e, nil
}
