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
	if tx.obsKeys != nil {
		_, ok := tx.obsKeys[obsDedupKey{observerID, pathJSON}]
		return ok
	}
	for _, o := range tx.Observations {
		if o.ObserverID == observerID && o.PathJSON == pathJSON {
			return true
		}
	}
	return false
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
		tx.obsKeys[obsDedupKey{obs.ObserverID, obs.PathJSON}] = struct{}{}
		if obs.ObserverID != "" {
			tx.observerSet[obs.ObserverID] = struct{}{}
		}
	case len(tx.Observations) >= obsIndexThreshold:
		tx.obsKeys = make(map[obsDedupKey]struct{}, 2*len(tx.Observations))
		tx.observerSet = make(map[string]struct{}, len(tx.Observations))
		for _, o := range tx.Observations {
			tx.obsKeys[obsDedupKey{o.ObserverID, o.PathJSON}] = struct{}{}
			if o.ObserverID != "" {
				tx.observerSet[o.ObserverID] = struct{}{}
			}
		}
	}
}
