package main

// Behavioral guard tests for docker-compose.staging.yml after the
// standalone mqtt-broker container was provisioned on staging.
//
// These tests assert the runtime SHAPE of the staging compose file —
// not its byte-for-byte content. They protect three invariants
// required for the staging-go container to coexist with the
// out-of-band mqtt-broker container:
//
//  1. staging-go MUST NOT publish host port 1883. The standalone
//     broker owns MQTT on the host; staging-go only needs intra-
//     network access via the meshcore-net docker network. A bound
//     1883:1883 mapping is at best dead weight, at worst a conflict
//     when the broker eventually moves to the host port.
//  2. The DISABLE_MOSQUITTO environment variable MUST default to
//     true so the in-container mosquitto is OFF unless an operator
//     explicitly opts back in. Otherwise we burn RAM running a
//     redundant broker that the ingestor isn't even pointed at.
//  3. The external docker network "meshcore-net" MUST be declared
//     and staging-go MUST be attached to it. That's how the
//     ingestor resolves "mqtt-broker:1883" via docker DNS.
//
// We assert shape via regex, not byte-equality, so cosmetic edits
// (comments, ordering, env var name additions) don't break the test.
// Use of any YAML parsing library is intentionally avoided here —
// cmd/server already has zero yaml deps and this test is meant to
// run as part of the normal `go test ./...` invocation in CI.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readStagingCompose(t *testing.T) string {
	t.Helper()
	// cmd/server -> repo root
	path := filepath.Join("..", "..", "docker-compose.staging.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// extractStagingGoBlock returns the YAML lines belonging to the
// services.staging-go entry. It stops at the next top-level
// services key or at a top-level key like "volumes:"/"networks:".
func extractStagingGoBlock(t *testing.T, yaml string) string {
	t.Helper()
	lines := strings.Split(yaml, "\n")
	var out []string
	in := false
	for _, ln := range lines {
		if !in {
			if strings.HasPrefix(ln, "  staging-go:") {
				in = true
				out = append(out, ln)
			}
			continue
		}
		// End of block: next service (2-space indent) or new top-level key (0-space).
		if len(ln) > 0 && !strings.HasPrefix(ln, "   ") && !strings.HasPrefix(ln, "    ") {
			// Allow blank lines mid-block; only stop on a real key.
			if strings.HasPrefix(ln, "  ") && strings.HasSuffix(strings.TrimSpace(ln), ":") {
				break
			}
			if !strings.HasPrefix(ln, " ") && strings.HasSuffix(strings.TrimSpace(ln), ":") {
				break
			}
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

func TestStagingCompose_NoHostPort1883(t *testing.T) {
	yaml := readStagingCompose(t)
	block := extractStagingGoBlock(t, yaml)
	// Match either literal "1883:1883" or the env-defaulted form.
	re := regexp.MustCompile(`(?m)^\s*-\s*"[^"]*:1883"\s*$`)
	if m := re.FindString(block); m != "" {
		t.Fatalf("staging-go must not bind host port 1883 (standalone mqtt-broker owns MQTT); found: %q", strings.TrimSpace(m))
	}
}

func TestStagingCompose_DisableMosquittoDefaultsTrue(t *testing.T) {
	yaml := readStagingCompose(t)
	block := extractStagingGoBlock(t, yaml)
	// Must contain a DISABLE_MOSQUITTO entry whose default is true.
	// Acceptable shapes:
	//   - DISABLE_MOSQUITTO=${DISABLE_MOSQUITTO:-true}
	//   - DISABLE_MOSQUITTO=true
	re := regexp.MustCompile(`DISABLE_MOSQUITTO=(\$\{DISABLE_MOSQUITTO:-true\}|true)(?:\s|$)`)
	if !re.MatchString(block) {
		t.Fatalf("staging-go DISABLE_MOSQUITTO must default to true (standalone broker is authoritative on staging); block:\n%s", block)
	}
}

func TestStagingCompose_MeshcoreNetExternalDeclared(t *testing.T) {
	yaml := readStagingCompose(t)
	// Top-level networks: section must declare meshcore-net as external.
	// We look for the network name + external: true within a small window.
	netRe := regexp.MustCompile(`(?ms)^networks:\s*\n(?:(?:[ \t]+#.*|\s*)\n)*[ \t]+meshcore-net:\s*\n(?:[ \t]+.+\n){1,6}`)
	m := netRe.FindString(yaml)
	if m == "" {
		t.Fatalf("top-level networks: must declare meshcore-net; yaml had no such block")
	}
	if !strings.Contains(m, "external: true") {
		t.Fatalf("meshcore-net must be declared external: true (the broker owns it); got:\n%s", m)
	}
}

func TestStagingCompose_StagingGoAttachedToMeshcoreNet(t *testing.T) {
	yaml := readStagingCompose(t)
	block := extractStagingGoBlock(t, yaml)
	// Look for a networks: child under staging-go that references meshcore-net.
	// Two acceptable shapes:
	//   networks:
	//     - meshcore-net
	//   networks:
	//     meshcore-net: {}
	netChild := regexp.MustCompile(`(?m)^\s{4}networks:\s*$`)
	if !netChild.MatchString(block) {
		t.Fatalf("staging-go must declare a networks: section to attach to meshcore-net; block:\n%s", block)
	}
	if !regexp.MustCompile(`meshcore-net`).MatchString(block) {
		t.Fatalf("staging-go networks: must reference meshcore-net; block:\n%s", block)
	}
}
