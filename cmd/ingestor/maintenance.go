package main

// PruneOldPackets deletes transmissions (and their observations) older
// than the given number of days. Returns count of transmissions deleted.
// Owned by the ingestor per #1283 (the writer process).
//
// Stub: real implementation lands in the GREEN commit.
func (s *Store) PruneOldPackets(days int) (int64, error) {
	return 0, nil
}
