// Package perfio holds the canonical PerfIOSample type shared between the
// ingestor (which publishes /proc/self/io rate samples to its on-disk stats
// file) and the server (which reads that file and surfaces the sample under
// /api/perf/io's `ingestor` block). Sharing the type prevents silent JSON
// contract drift if a field is added on one side only.
package perfio

// Sample is the per-process I/O rate sample written by the ingestor and
// consumed by the server. Field names + json tags MUST be considered the
// stable on-disk contract — adding/renaming a field is a breaking change.
type Sample struct {
	ReadBytesPerSec           float64 `json:"readBytesPerSec"`
	WriteBytesPerSec          float64 `json:"writeBytesPerSec"`
	CancelledWriteBytesPerSec float64 `json:"cancelledWriteBytesPerSec"`
	SyscallsRead              float64 `json:"syscallsRead"`
	SyscallsWrite             float64 `json:"syscallsWrite"`
	SampledAt                 string  `json:"sampledAt,omitempty"`
}
