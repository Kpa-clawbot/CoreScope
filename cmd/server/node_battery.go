package main

// BatteryThresholdsConfig is a stub; default values returned by Config getters.
type BatteryThresholdsConfig struct {
	LowMv      int `json:"lowMv"`
	CriticalMv int `json:"criticalMv"`
}

// LowBatteryMv stub: always returns default 3300.
func (c *Config) LowBatteryMv() int { return 0 }

// CriticalBatteryMv stub: always returns default 3000.
func (c *Config) CriticalBatteryMv() int { return 0 }

// NodeBatterySample stub.
type NodeBatterySample struct {
	Timestamp string `json:"timestamp"`
	BatteryMv int    `json:"battery_mv"`
}

// GetNodeBatteryHistory stub: always returns empty.
func (db *DB) GetNodeBatteryHistory(pubkey, since string) ([]NodeBatterySample, error) {
	return nil, nil
}
