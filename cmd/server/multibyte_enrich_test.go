package main

import "testing"

func TestEnrichNodeWithMultiByte(t *testing.T) {
	t.Run("nil entry leaves no fields", func(t *testing.T) {
		node := map[string]interface{}{"public_key": "abc123"}
		EnrichNodeWithMultiByte(node, nil)
		if _, ok := node["multi_byte_status"]; ok {
			t.Error("expected no multi_byte_status with nil entry")
		}
	})

	t.Run("confirmed entry sets fields", func(t *testing.T) {
		node := map[string]interface{}{"public_key": "abc123"}
		entry := &MultiByteCapEntry{
			Status:      "confirmed",
			Evidence:    "advert",
			MaxHashSize: 2,
		}
		EnrichNodeWithMultiByte(node, entry)
		if node["multi_byte_status"] != "confirmed" {
			t.Errorf("expected confirmed, got %v", node["multi_byte_status"])
		}
		if node["multi_byte_evidence"] != "advert" {
			t.Errorf("expected advert, got %v", node["multi_byte_evidence"])
		}
		if node["multi_byte_max_hash_size"] != 2 {
			t.Errorf("expected 2, got %v", node["multi_byte_max_hash_size"])
		}
	})

	t.Run("suspected entry sets fields", func(t *testing.T) {
		node := map[string]interface{}{"public_key": "abc123"}
		entry := &MultiByteCapEntry{
			Status:      "suspected",
			Evidence:    "path",
			MaxHashSize: 2,
		}
		EnrichNodeWithMultiByte(node, entry)
		if node["multi_byte_status"] != "suspected" {
			t.Errorf("expected suspected, got %v", node["multi_byte_status"])
		}
	})

	t.Run("unknown entry sets status unknown", func(t *testing.T) {
		node := map[string]interface{}{"public_key": "abc123"}
		entry := &MultiByteCapEntry{
			Status:      "unknown",
			MaxHashSize: 1,
		}
		EnrichNodeWithMultiByte(node, entry)
		if node["multi_byte_status"] != "unknown" {
			t.Errorf("expected unknown, got %v", node["multi_byte_status"])
		}
	})
}
