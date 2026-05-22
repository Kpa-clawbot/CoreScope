// store_channels.go — channel listing, messages and channel analytics.

package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GetChannels returns channel list from in-memory packets (payload_type 5, decoded type CHAN).
func (s *PacketStore) GetChannels(region string) []map[string]interface{} {
	cacheKey := region

	s.channelsCacheMu.Lock()
	if s.channelsCacheRes != nil && s.channelsCacheKey == cacheKey && time.Now().Before(s.channelsCacheExp) {
		res := s.channelsCacheRes
		s.channelsCacheMu.Unlock()
		return res
	}
	s.channelsCacheMu.Unlock()

	type txSnapshot struct {
		firstSeen   string
		decodedJSON string
		hasRegion   bool
	}

	// Resolve region→observer set before taking s.mu so a cache-miss SQL
	// query does not run under the read lock (review item #2).
	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}

	// Copy only the fields needed — release the lock before JSON unmarshal.
	s.mu.RLock()
	grpTxts := s.byPayloadType[5]
	snapshots := make([]txSnapshot, 0, len(grpTxts))
	for _, tx := range grpTxts {
		inRegion := true
		if regionObs != nil {
			inRegion = false
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					inRegion = true
					break
				}
			}
		}
		snapshots = append(snapshots, txSnapshot{
			firstSeen:   tx.FirstSeen,
			decodedJSON: tx.DecodedJSON,
			hasRegion:   inRegion,
		})
	}
	s.mu.RUnlock()

	// JSON unmarshal outside the lock.
	type chanInfo struct {
		Hash         string
		Name         string
		LastMessage  interface{}
		LastSender   interface{}
		MessageCount int
		LastActivity string
	}
	type decodedGrp struct {
		Type    string `json:"type"`
		Channel string `json:"channel"`
		Text    string `json:"text"`
		Sender  string `json:"sender"`
	}
	channelMap := map[string]*chanInfo{}
	for _, snap := range snapshots {
		if !snap.hasRegion {
			continue
		}
		var decoded decodedGrp
		if json.Unmarshal([]byte(snap.decodedJSON), &decoded) != nil {
			continue
		}
		if decoded.Type != "CHAN" {
			continue
		}
		if hasGarbageChars(decoded.Channel) || hasGarbageChars(decoded.Text) {
			continue
		}
		channelName := decoded.Channel
		if channelName == "" {
			channelName = "unknown"
		}
		ch := channelMap[channelName]
		if ch == nil {
			ch = &chanInfo{Hash: channelName, Name: channelName, LastActivity: snap.firstSeen}
			channelMap[channelName] = ch
		}
		ch.MessageCount++
		if snap.firstSeen >= ch.LastActivity {
			ch.LastActivity = snap.firstSeen
			if decoded.Text != "" {
				idx := strings.Index(decoded.Text, ": ")
				if idx > 0 {
					ch.LastMessage = decoded.Text[idx+2:]
				} else {
					ch.LastMessage = decoded.Text
				}
				if decoded.Sender != "" {
					ch.LastSender = decoded.Sender
				}
			}
		}
	}

	channels := make([]map[string]interface{}, 0, len(channelMap))
	for _, ch := range channelMap {
		channels = append(channels, map[string]interface{}{
			"hash": ch.Hash, "name": ch.Name,
			"lastMessage": ch.LastMessage, "lastSender": ch.LastSender,
			"messageCount": ch.MessageCount, "lastActivity": ch.LastActivity,
		})
	}

	// #688: scan decoded message text for #hashtag mentions and surface any
	// previously-unseen channel names as discovered channels. We dedup against
	// channelMap (matched by name) so a channel that already has traffic does
	// NOT also appear as discovered.
	discovered := map[string]string{} // name -> lastActivity
	for _, snap := range snapshots {
		if !snap.hasRegion {
			continue
		}
		var decoded decodedGrp
		if json.Unmarshal([]byte(snap.decodedJSON), &decoded) != nil {
			continue
		}
		if decoded.Type != "CHAN" || decoded.Text == "" {
			continue
		}
		if hasGarbageChars(decoded.Text) {
			continue
		}
		for _, tag := range extractHashtagsFromText(decoded.Text) {
			// Skip if already a known/decoded channel (by name with or without '#').
			bare := tag[1:]
			if _, ok := channelMap[tag]; ok {
				continue
			}
			if _, ok := channelMap[bare]; ok {
				continue
			}
			if existing, ok := discovered[tag]; !ok || snap.firstSeen > existing {
				discovered[tag] = snap.firstSeen
			}
		}
	}
	for name, lastActivity := range discovered {
		channels = append(channels, map[string]interface{}{
			"hash":         name,
			"name":         name,
			"lastMessage":  nil,
			"lastSender":   nil,
			"messageCount": 0,
			"lastActivity": lastActivity,
			"discovered":   true,
		})
	}

	s.channelsCacheMu.Lock()
	s.channelsCacheRes = channels
	s.channelsCacheKey = cacheKey
	s.channelsCacheExp = time.Now().Add(15 * time.Second)
	s.channelsCacheMu.Unlock()

	return channels
}

// GetEncryptedChannels returns undecryptable GRP_TXT channels from in-memory packets.
func (s *PacketStore) GetEncryptedChannels(region string) []map[string]interface{} {
	// Resolve region→observer set before taking s.mu so a cache-miss SQL
	// query does not run under the read lock (review item #2).
	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}

	s.mu.RLock()
	grpTxts := s.byPayloadType[5]

	type encInfo struct {
		hash         string
		messageCount int
		lastActivity string
	}
	type grpDec struct {
		Type             string      `json:"type"`
		ChannelHash      interface{} `json:"channelHash"`
		ChannelHashHex   string      `json:"channelHashHex"`
		DecryptionStatus string      `json:"decryptionStatus"`
	}
	channelMap := map[string]*encInfo{}

	for _, tx := range grpTxts {
		if regionObs != nil {
			match := false
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		var decoded grpDec
		if json.Unmarshal([]byte(tx.DecodedJSON), &decoded) != nil {
			continue
		}
		if decoded.Type != "GRP_TXT" || decoded.DecryptionStatus != "no_key" {
			continue
		}
		chHash := decoded.ChannelHashHex
		if chHash == "" {
			if num, ok := decoded.ChannelHash.(float64); ok {
				chHash = fmt.Sprintf("%02X", int(num))
			}
		}
		if chHash == "" {
			chHash = "?"
		}
		ch := channelMap[chHash]
		if ch == nil {
			ch = &encInfo{hash: chHash, lastActivity: tx.FirstSeen}
			channelMap[chHash] = ch
		}
		ch.messageCount++
		if tx.FirstSeen >= ch.lastActivity {
			ch.lastActivity = tx.FirstSeen
		}
	}
	s.mu.RUnlock()

	channels := make([]map[string]interface{}, 0, len(channelMap))
	for _, ch := range channelMap {
		channels = append(channels, map[string]interface{}{
			"hash":         "enc_" + ch.hash,
			"name":         "Encrypted (0x" + ch.hash + ")",
			"lastMessage":  nil,
			"lastSender":   nil,
			"messageCount": ch.messageCount,
			"lastActivity": ch.lastActivity,
			"encrypted":    true,
		})
	}
	return channels
}

// GetChannelMessages returns deduplicated messages for a channel from in-memory packets.
func (s *PacketStore) GetChannelMessages(channelHash string, limit, offset int, region ...string) ([]map[string]interface{}, int) {
	regionParam := ""
	if len(region) > 0 {
		regionParam = region[0]
	}
	// Resolve region→observer set before taking s.mu so a cache-miss SQL
	// query does not run under the read lock (review item #2).
	regionObs := s.resolveRegionObservers(regionParam)

	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}

	type msgEntry struct {
		Data      map[string]interface{}
		Repeats   int
		Observers []string
	}
	msgMap := map[string]*msgEntry{}
	var msgOrder []string

	// Iterate type-5 packets oldest-first (byPayloadType is ASC = oldest first)
	type decodedMsg struct {
		Type            string      `json:"type"`
		Channel         string      `json:"channel"`
		Text            string      `json:"text"`
		Sender          string      `json:"sender"`
		SenderTimestamp interface{} `json:"sender_timestamp"`
		PathLen         int         `json:"path_len"`
	}

	grpTxts := s.byPayloadType[5]
	for _, tx := range grpTxts {
		if regionObs != nil {
			match := false
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}

		if tx.DecodedJSON == "" {
			continue
		}

		var decoded decodedMsg
		if json.Unmarshal([]byte(tx.DecodedJSON), &decoded) != nil {
			continue
		}
		if decoded.Type != "CHAN" {
			continue
		}
		ch := decoded.Channel
		if ch == "" {
			ch = "unknown"
		}
		if ch != channelHash {
			continue
		}

		text := decoded.Text
		sender := decoded.Sender
		if sender == "" && text != "" {
			idx := strings.Index(text, ": ")
			if idx > 0 && idx < 50 {
				sender = text[:idx]
			}
		}

		dedupeKey := sender + ":" + tx.Hash

		if existing, ok := msgMap[dedupeKey]; ok {
			existing.Repeats++
			existing.Data["repeats"] = existing.Repeats
			// Add observer if new
			obsName := tx.ObserverName
			if obsName == "" {
				obsName = tx.ObserverID
			}
			if obsName != "" {
				found := false
				for _, o := range existing.Observers {
					if o == obsName {
						found = true
						break
					}
				}
				if !found {
					existing.Observers = append(existing.Observers, obsName)
					existing.Data["observers"] = existing.Observers
				}
			}
		} else {
			displaySender := sender
			displayText := text
			if text != "" {
				idx := strings.Index(text, ": ")
				if idx > 0 && idx < 50 {
					displaySender = text[:idx]
					displayText = text[idx+2:]
				}
			}

			hops := pathLen(tx.PathJSON)

			var snrVal interface{}
			if tx.SNR != nil {
				snrVal = *tx.SNR
			}

			senderTs := decoded.SenderTimestamp

			observers := []string{}
			obsName := tx.ObserverName
			if obsName == "" {
				obsName = tx.ObserverID
			}
			if obsName != "" {
				observers = []string{obsName}
			}

			entry := &msgEntry{
				Data: map[string]interface{}{
					"sender":           displaySender,
					"text":             displayText,
					"timestamp":        strOrNil(tx.FirstSeen),
					"sender_timestamp": senderTs,
					"packetId":         tx.ID,
					"packetHash":       strOrNil(tx.Hash),
					"repeats":          1,
					"observers":        observers,
					"hops":             hops,
					"snr":              snrVal,
				},
				Repeats:   1,
				Observers: observers,
			}
			msgMap[dedupeKey] = entry
			msgOrder = append(msgOrder, dedupeKey)
		}
	}

	total := len(msgOrder)
	// Return latest messages (tail)
	start := total - limit - offset
	if start < 0 {
		start = 0
	}
	end := total - offset
	if end < 0 {
		end = 0
	}
	if end > total {
		end = total
	}

	messages := make([]map[string]interface{}, 0, end-start)
	for i := start; i < end; i++ {
		messages = append(messages, msgMap[msgOrder[i]].Data)
	}
	return messages, total
}

// GetAnalyticsChannels returns full channel analytics computed from in-memory packets.
func (s *PacketStore) GetAnalyticsChannels(region string) map[string]interface{} {
	return s.GetAnalyticsChannelsWithWindow(region, TimeWindow{})
}

// GetAnalyticsChannelsWithWindow returns channel analytics for the given region,
// optionally bounded to a time window (issue #842). Zero TimeWindow = all data.
// For the default query (region="", zero window) the steady-state recomputer
// snapshot (issue #1240) is preferred — atomic load, never blocks.
func (s *PacketStore) GetAnalyticsChannelsWithWindow(region string, window TimeWindow) map[string]interface{} {
	if region == "" && window.IsZero() {
		s.analyticsRecomputerMu.RLock()
		rc := s.recompChannels
		s.analyticsRecomputerMu.RUnlock()
		if rc != nil {
			if v := rc.Load(); v != nil {
				if m, ok := v.(map[string]interface{}); ok {
					s.cacheMu.Lock()
					s.cacheHits++
					s.cacheMu.Unlock()
					return m
				}
			}
		}
	}
	cacheKey := region
	if !window.IsZero() {
		cacheKey = region + "|" + window.CacheKey()
	}
	s.cacheMu.Lock()
	if cached, ok := s.chanCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsChannels(region, window)

	s.cacheMu.Lock()
	s.chanCache[cacheKey] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

// channelNameMatchesHash validates that a decrypted channel name hashes to the
// observed single-byte channel hash. This rejects rainbow-table mismatches where
// an observer's lookup table incorrectly maps a hash byte to the wrong name.
// Firmware invariant: channelHash = SHA256(SHA256("#name")[:16])[0]
func channelNameMatchesHash(name string, hashStr string) bool {
	expected, err := strconv.Atoi(hashStr)
	if err != nil {
		return false
	}
	chanName := name
	if !strings.HasPrefix(chanName, "#") {
		chanName = "#" + chanName
	}
	h1 := sha256.Sum256([]byte(chanName))
	h2 := sha256.Sum256(h1[:16])
	return int(h2[0]) == expected
}

// isPlaceholderName returns true if the name is a "chN" placeholder (not a real decrypted name).
func isPlaceholderName(name string) bool {
	if !strings.HasPrefix(name, "ch") {
		return false
	}
	_, err := strconv.Atoi(name[2:])
	return err == nil
}

func (s *PacketStore) computeAnalyticsChannels(region string, window TimeWindow) map[string]interface{} {
	// Resolve region→observer set before taking s.mu so a cache-miss SQL
	// query does not run under the read lock (review item #2).
	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	type decodedGrp struct {
		Type         string      `json:"type"`
		Channel      string      `json:"channel"`
		ChannelHash  interface{} `json:"channelHash"`
		ChannelHash2 string      `json:"channel_hash"`
		Text         string      `json:"text"`
		Sender       string      `json:"sender"`
	}

	// Convert channelHash (number or string in JSON) to string
	chHashStr := func(v interface{}) string {
		if v == nil {
			return ""
		}
		switch val := v.(type) {
		case string:
			return val
		case float64:
			return strconv.FormatFloat(val, 'f', -1, 64)
		default:
			return fmt.Sprintf("%v", val)
		}
	}

	type chanInfo struct {
		Hash         string
		Name         string
		Messages     int
		Senders      map[string]bool
		LastActivity string
		Encrypted    bool
	}

	channelMap := map[string]*chanInfo{}
	senderCounts := map[string]int{}
	msgLengths := make([]int, 0)
	timeline := map[string]int{} // hour|channelName → count

	grpTxts := s.byPayloadType[5]
	for _, tx := range grpTxts {
		if !window.Includes(tx.FirstSeen) {
			continue
		}
		if regionObs != nil {
			match := false
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}

		var decoded decodedGrp
		if json.Unmarshal([]byte(tx.DecodedJSON), &decoded) != nil {
			continue
		}

		hash := chHashStr(decoded.ChannelHash)
		if hash == "" {
			hash = decoded.ChannelHash2
		}
		if hash == "" {
			hash = "?"
		}
		name := decoded.Channel
		if name == "" {
			name = "ch" + hash
		}
		encrypted := decoded.Text == "" && decoded.Sender == ""

		// Bug #978 fix: validate channel name against hash to reject rainbow-table mismatches.
		// If the claimed channel name doesn't hash to the observed channelHash byte, discard it.
		if name != "" && name != "ch"+hash && !channelNameMatchesHash(name, hash) {
			name = "ch" + hash
			encrypted = true
		}

		// Bug #978 fix: always group by hash byte alone — same physical channel,
		// regardless of which observer decrypted it.
		chKey := hash

		ch := channelMap[chKey]
		if ch == nil {
			ch = &chanInfo{Hash: hash, Name: name, Senders: map[string]bool{}, LastActivity: tx.FirstSeen, Encrypted: encrypted}
			channelMap[chKey] = ch
		} else {
			// Upgrade bucket name: if current is placeholder and we have a validated decrypted name
			if isPlaceholderName(ch.Name) && !isPlaceholderName(name) {
				ch.Name = name
			}
		}
		ch.Messages++
		ch.LastActivity = tx.FirstSeen
		if !encrypted {
			ch.Encrypted = false
		}

		if decoded.Sender != "" {
			ch.Senders[decoded.Sender] = true
			senderCounts[decoded.Sender]++
		}
		if decoded.Text != "" {
			msgLengths = append(msgLengths, len(decoded.Text))
		}

		// Timeline
		if len(tx.FirstSeen) >= 13 {
			hr := tx.FirstSeen[:13]
			key := hr + "|" + name
			timeline[key]++
		}
	}

	channelList := make([]map[string]interface{}, 0, len(channelMap))
	decryptable := 0
	for _, c := range channelMap {
		if !c.Encrypted {
			decryptable++
		}
		channelList = append(channelList, map[string]interface{}{
			"hash": c.Hash, "name": c.Name,
			"messages": c.Messages, "senders": len(c.Senders),
			"lastActivity": c.LastActivity, "encrypted": c.Encrypted,
		})
	}
	sort.Slice(channelList, func(i, j int) bool {
		return channelList[i]["messages"].(int) > channelList[j]["messages"].(int)
	})

	// Top senders
	type senderEntry struct {
		name  string
		count int
	}
	senderList := make([]senderEntry, 0, len(senderCounts))
	for n, c := range senderCounts {
		senderList = append(senderList, senderEntry{n, c})
	}
	sort.Slice(senderList, func(i, j int) bool { return senderList[i].count > senderList[j].count })
	topSenders := make([]map[string]interface{}, 0)
	for i, e := range senderList {
		if i >= 15 {
			break
		}
		topSenders = append(topSenders, map[string]interface{}{"name": e.name, "count": e.count})
	}

	// Channel timeline
	type tlEntry struct {
		hour, channel string
		count         int
	}
	var tlList []tlEntry
	for key, count := range timeline {
		parts := strings.SplitN(key, "|", 2)
		if len(parts) == 2 {
			tlList = append(tlList, tlEntry{parts[0], parts[1], count})
		}
	}
	sort.Slice(tlList, func(i, j int) bool { return tlList[i].hour < tlList[j].hour })
	channelTimeline := make([]map[string]interface{}, 0, len(tlList))
	for _, e := range tlList {
		channelTimeline = append(channelTimeline, map[string]interface{}{
			"hour": e.hour, "channel": e.channel, "count": e.count,
		})
	}

	return map[string]interface{}{
		"activeChannels":  len(channelList),
		"decryptable":     decryptable,
		"channels":        channelList,
		"topSenders":      topSenders,
		"channelTimeline": channelTimeline,
		"msgLengths":      msgLengths,
	}
}
