package dbconfig

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

func TestSelectionMaintenanceLeaseExcludesServicesWithoutChangingRecord(t *testing.T) {
	selected := sqliteSelection(t)
	path := SelectionPath(selected.StateDir)
	if _, err := AdoptSelection(path, selected); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, runtime, err := OpenSelection(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, lease, err := OpenSelectionForMaintenance(path); !errors.Is(err, ErrSelectionBusy) {
		if lease != nil {
			lease.Close()
		}
		runtime.Close()
		t.Fatal("maintenance accepted running service", err)
	}
	runtime.Close()
	got, lease, err := OpenSelectionForMaintenance(path)
	if err != nil || got.Generation != selected.Generation {
		t.Fatal("maintenance lease", err)
	}
	defer lease.Close()
	if _, runtime, err := OpenSelection(path); !errors.Is(err, ErrSelectionBusy) {
		if runtime != nil {
			runtime.Close()
		}
		t.Fatal("service started during maintenance", err)
	}
	lease.Close()
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("maintenance changed selection", err)
	}
}
