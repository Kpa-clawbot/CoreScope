package main

// Behavioral guard tests for docker-compose.staging.yml after the
// standalone mqtt-broker container was provisioned on staging.
//
// These tests assert the runtime SHAPE of the staging compose file —
// not its byte-for-byte content. They protect three invariants
// required for the staging-go container to coexist with the
// out-of-band mqtt-broker container:
//
//  1. staging-go MUST NOT publish host port 1883 in ANY form (short,
//     long, quoted, unquoted). The standalone broker owns MQTT on
//     the host; staging-go only needs intra-network access via the
//     meshcore-net docker network. A bound 1883 mapping is at best
//     dead weight, at worst a conflict when the broker eventually
//     moves to the host port.
//  2. The DISABLE_MOSQUITTO environment variable MUST use the
//     interpolated default form `${DISABLE_MOSQUITTO:-true}` so the
//     in-container mosquitto is OFF unless an operator explicitly
//     opts back in via env, while still preserving that override
//     capability. Bare literal `true` (no override path) or any
//     later `=false` override under staging-go is rejected.
//  3. The external docker network "meshcore-net" MUST be declared
//     and staging-go MUST be attached to it via a real
//     services.staging-go.networks sub-key (not merely mentioned
//     anywhere in the block, e.g. in a comment). That's how the
//     ingestor resolves "mqtt-broker:1883" via docker DNS.
//
// We assert shape via regex, not byte-equality, so cosmetic edits
// (comments, ordering, env var name additions) don't break the test.
// Comment lines are stripped before matching so a `# 1883:1883`
// example in prose cannot masquerade as a real port binding.
//
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

// stripYAMLComments removes any `#`-prefixed comment tail (and pure
// comment lines) so downstream regex matches only see YAML data,
// not prose examples embedded in comments.
func stripYAMLComments(yaml string) string {
	lines := strings.Split(yaml, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if i := strings.Index(ln, "#"); i >= 0 {
			ln = ln[:i]
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
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

// extractSubBlock returns the indented body under a given `key:` line
// inside `block`. `keyIndent` is the number of leading spaces expected
// on the key line (e.g. 4 for a service-level key under staging-go).
// The returned block excludes the key line itself and stops at the
// first line whose indent is <= keyIndent (i.e. a sibling or shallower
// key).
func extractSubBlock(block, key string, keyIndent int) string {
	lines := strings.Split(block, "\n")
	keyLine := strings.Repeat(" ", keyIndent) + key + ":"
	var out []string
	in := false
	for _, ln := range lines {
		if !in {
			if strings.HasPrefix(ln, keyLine) {
				in = true
			}
			continue
		}
		if strings.TrimSpace(ln) == "" {
			out = append(out, ln)
			continue
		}
		// Count leading spaces.
		lead := 0
		for lead < len(ln) && ln[lead] == ' ' {
			lead++
		}
		if lead <= keyIndent {
			break
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

func TestStagingCompose_NoHostPort1883(t *testing.T) {
	yaml := readStagingCompose(t)
	block := extractStagingGoBlock(t, yaml)
	// Restrict to the ports: sub-block so unrelated 1883 tokens
	// elsewhere (unlikely, but future-proof) cannot trigger.
	// Strip comments FIRST so an in-comment example cannot mask or
	// masquerade as a binding.
	portsBlock := extractSubBlock(stripYAMLComments(block), "ports", 4)

	// Reject ANY 1883 target under ports:
	//   - "1883:1883"                     (quoted short form, either side is 1883)
	//   - 1883:1883                       (unquoted short form)
	//   - "<any>:1883"                    (quoted, host:1883)
	//   - target: 1883 / published: 1883  (long form)
	patterns := []*regexp.Regexp{
		// Short form list item (quoted or unquoted); require a colon separator
		// so "1883" appearing alone in a comment-stripped ancillary context
		// isn't mistaken (it wouldn't legally be a mapping anyway).
		regexp.MustCompile(`(?m)^\s*-\s*"?[^"\s]*:1883"?\s*$`),
		regexp.MustCompile(`(?m)^\s*-\s*"?1883:[^"\s]+"?\s*$`),
		// Long form: target: 1883 or published: 1883
		regexp.MustCompile(`(?m)^\s*(target|published)\s*:\s*"?1883"?\s*$`),
	}
	for _, re := range patterns {
		if m := re.FindString(portsBlock); m != "" {
			t.Fatalf("staging-go must not bind port 1883 in any form (standalone mqtt-broker owns MQTT); found: %q\nports block:\n%s", strings.TrimSpace(m), portsBlock)
		}
	}
}

func TestStagingCompose_DisableMosquittoDefaultsTrue(t *testing.T) {
	yaml := readStagingCompose(t)
	block := extractStagingGoBlock(t, yaml)
	// Restrict to the environment: sub-block after stripping
	// comments so a `# DISABLE_MOSQUITTO=true` prose example
	// can't satisfy the assertion, and the "first-match anywhere"
	// bug is closed off.
	envBlock := extractSubBlock(stripYAMLComments(block), "environment", 4)

	// Required shape: the interpolated form that preserves override.
	//   - DISABLE_MOSQUITTO=${DISABLE_MOSQUITTO:-true}
	// Bare `DISABLE_MOSQUITTO=true` is REJECTED — it removes the
	// operator's ability to opt in without editing the compose file,
	// which is the shape the PR body promises.
	want := regexp.MustCompile(`(?m)DISABLE_MOSQUITTO=\$\{DISABLE_MOSQUITTO:-true\}\s*$`)
	if !want.MatchString(envBlock) {
		t.Fatalf("staging-go must declare `DISABLE_MOSQUITTO=${DISABLE_MOSQUITTO:-true}` (interpolated form preserves override capability); env block:\n%s", envBlock)
	}

	// Guard against a later `=false` override in the same env block.
	// Any additional DISABLE_MOSQUITTO assignment with a `false`
	// default (interpolated or literal) undoes the intent.
	bad := regexp.MustCompile(`(?m)DISABLE_MOSQUITTO=(?:\$\{DISABLE_MOSQUITTO:-false\}|false)\s*$`)
	if m := bad.FindString(envBlock); m != "" {
		t.Fatalf("staging-go env must not include a DISABLE_MOSQUITTO=false override (default MUST be true); found: %q", strings.TrimSpace(m))
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
	// Attach-check must find meshcore-net as a real entry in the
	// services.staging-go.networks: sub-block, NOT anywhere in the
	// service block (which would match comment lines like
	// "# … meshcore-net docker network below.").
	networksBlock := extractSubBlock(stripYAMLComments(block), "networks", 4)
	if strings.TrimSpace(networksBlock) == "" {
		t.Fatalf("staging-go must declare a networks: section to attach to meshcore-net; block:\n%s", block)
	}
	// Two acceptable shapes (both, comment-stripped):
	//   networks:
	//     - meshcore-net
	//   networks:
	//     meshcore-net: {}
	shortForm := regexp.MustCompile(`(?m)^\s*-\s*meshcore-net\s*$`)
	longForm := regexp.MustCompile(`(?m)^\s*meshcore-net\s*:\s*(\{\s*\}\s*)?$`)
	if !shortForm.MatchString(networksBlock) && !longForm.MatchString(networksBlock) {
		t.Fatalf("staging-go.networks: must reference meshcore-net as a real entry (list item or subkey), not just in prose; networks block:\n%s", networksBlock)
	}
}
