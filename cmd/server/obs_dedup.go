package main

// obsDedupKey identifies an observation within its transmission: the same
// observer reporting the same path is a duplicate.
type obsDedupKey struct{ observerID, pathJSON string }

// obsIndexThreshold is the observation count at which a transmission starts
// keeping maps for its duplicate and distinct-observer checks. Below it the
// checks scan tx.Observations, which compares string headers and costs
// nothing to keep; the maps cost a few hundred bytes per transmission, and
// most transmissions are heard a handful of times (a week of a busy regional
// network peaked at 40). A miss scans in about 150ns at 40 observations and
// 240ns at 63; past the threshold a map lookup is about 15ns. The maps' keys
// share the observations' strings, so building them allocates no key bytes.
const obsIndexThreshold = 64

// hasObservation reports whether tx already holds an observation from
// observerID with pathJSON.
func (tx *StoreTx) hasObservation(observerID, pathJSON string) bool {
	return tx.observation(observerID, pathJSON) != nil
}

func (tx *StoreTx) observation(observerID, pathJSON string) *StoreObs {
	if tx.obsKeys != nil {
		return tx.obsKeys[obsDedupKey{observerID, pathJSON}]
	}
	for _, o := range tx.Observations {
		if o.ObserverID == observerID && o.PathJSON == pathJSON {
			return o
		}
	}
	return nil
}

// mergeObservationEvidence preserves an eligible flood when observer/path
// dedup merges frames with different headers. The validated path fixes its
// width, so known evidence can only upgrade unknown/ineligible evidence.
// Caller owns tx privately during load or holds s.mu for writing.
func (tx *StoreTx) mergeObservationEvidence(observerID, pathJSON string, n uint8) *StoreObs {
	obs := tx.observation(observerID, pathJSON)
	if obs != nil && n != 0 {
		obs.relayHopHexLen = n
	}
	return obs
}

// hasObserver reports whether any observation on tx came from observerID.
func (tx *StoreTx) hasObserver(observerID string) bool {
	if tx.observerSet != nil {
		_, ok := tx.observerSet[observerID]
		return ok
	}
	for _, o := range tx.Observations {
		if o.ObserverID == observerID {
			return true
		}
	}
	return false
}

// addObservation appends obs to tx and keeps UniqueObserverCount and
// ObservationCount in step. Callers check hasObservation first.
func (tx *StoreTx) addObservation(obs *StoreObs) {
	if obs.ObserverID != "" && !tx.hasObserver(obs.ObserverID) {
		tx.UniqueObserverCount++
	}
	tx.Observations = append(tx.Observations, obs)
	tx.ObservationCount++
	switch {
	case tx.obsKeys != nil:
		tx.obsKeys[obsDedupKey{obs.ObserverID, obs.PathJSON}] = obs
		if obs.ObserverID != "" {
			tx.observerSet[obs.ObserverID] = struct{}{}
		}
	case len(tx.Observations) >= obsIndexThreshold:
		tx.obsKeys = make(map[obsDedupKey]*StoreObs, 2*len(tx.Observations))
		tx.observerSet = make(map[string]struct{}, len(tx.Observations))
		for _, o := range tx.Observations {
			tx.obsKeys[obsDedupKey{o.ObserverID, o.PathJSON}] = o
			if o.ObserverID != "" {
				tx.observerSet[o.ObserverID] = struct{}{}
			}
		}
	}
}
