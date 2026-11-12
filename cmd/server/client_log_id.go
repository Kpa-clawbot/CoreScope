package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// clientLogKey keys clientLogID. It is random per process, so a client's ID is
// stable within a run (an operator can correlate its requests) but cannot be
// reversed or linked across restarts.
var clientLogKey = func() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic("client log key: " + err.Error())
	}
	return k
}()

// clientLogID names a client in logs without recording its IP: the first 12
// hex chars of HMAC-SHA256(clientLogKey, ip). The shared bucket has no IP to
// hide and logs as itself.
func clientLogID(ip string) string {
	if ip == clientSharedKey {
		return clientSharedKey
	}
	m := hmac.New(sha256.New, clientLogKey)
	m.Write([]byte(ip))
	return hex.EncodeToString(m.Sum(nil)[:6])
}
