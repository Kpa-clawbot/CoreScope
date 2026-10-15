package dbconfig

import (
	"encoding/json"
	"errors"
	"io"
	"os"
)

// ReadStorageConfig reads just the shared storage fields, including disabled
// account stores. It preserves raw paths; callers apply their explicit config
// discovery, environment precedence and ResolveStorage exactly once.
func ReadStorageConfig(path string) (StorageInputs, error) {
	f, err := os.Open(path)
	if err != nil {
		return StorageInputs{}, err
	}
	defer f.Close()
	var config *struct {
		DB          DBConfig `json:"db"`
		DBPath      string   `json:"dbPath"`
		DatabaseURL string   `json:"databaseURL"`
		StateDir    string   `json:"stateDir"`
		Users       struct {
			DBPath                      string `json:"dbPath"`
			DatabaseURL                 string `json:"databaseURL"`
			ApprovedChannelsDatabaseURL string `json:"approvedChannelsDatabaseURL"`
		} `json:"userManagement"`
	}
	const maxConfigBytes = 4 * 1024 * 1024
	limit := &io.LimitedReader{R: f, N: maxConfigBytes + 1}
	decoder := json.NewDecoder(limit)
	if err := decoder.Decode(&config); err != nil || config == nil {
		return StorageInputs{}, errors.New("invalid storage configuration JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF || limit.N <= 0 {
		return StorageInputs{}, errors.New("storage configuration exceeds its bound or has trailing JSON")
	}
	return StorageInputs{Backend: config.DB.Backend, DBPath: config.DBPath, DatabaseURL: config.DatabaseURL, StateDir: config.StateDir, UsersDBPath: config.Users.DBPath, UsersDatabaseURL: config.Users.DatabaseURL, ApprovedChannelsDatabaseURL: config.Users.ApprovedChannelsDatabaseURL}, nil
}
