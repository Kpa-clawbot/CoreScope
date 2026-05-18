package main

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// cpuSample is a snapshot of process CPU time (utime+stime) and the wall
// clock at which it was taken. Process CPU percent is the delta of CPU
// jiffies converted to seconds, divided by the elapsed wall time.
type cpuSample struct {
	at      time.Time
	cpuSecs float64
}

// cpuTracker keeps the previous /proc/self/stat sample so getCPUPercent can
// compute a delta. Mutex-guarded — getCPUPercent may run from concurrent
// requests and the background perf-sampling goroutine.
var (
	cpuMu         sync.Mutex
	cpuLastSample cpuSample
)

// clockTicksPerSec is the kernel's USER_HZ. /proc/self/stat reports utime and
// stime in clock ticks; Linux fixes this at 100 on essentially all modern
// builds. Go has no portable sysconf(_SC_CLK_TCK), so 100 is hardcoded —
// matching how perf_io.go reads /proc with Linux-only assumptions.
const clockTicksPerSec = 100.0

// getCPUPercent samples this process's CPU usage from /proc/self/stat
// (utime + stime jiffies) and returns the percentage of one core consumed
// since the previous call. On the first call (no prior sample) it returns 0.
// On non-Linux or when /proc/self/stat is unavailable/unparseable it returns
// 0 — diagnostics only, never errors. Mirrors readProcIO in perf_io.go.
func getCPUPercent() float64 {
	cur, ok := readProcCPU()
	if !ok {
		return 0
	}

	cpuMu.Lock()
	prev := cpuLastSample
	cpuLastSample = cur
	cpuMu.Unlock()

	if prev.at.IsZero() {
		return 0
	}
	dt := cur.at.Sub(prev.at).Seconds()
	if dt < 0.001 {
		return 0
	}
	pct := (cur.cpuSecs - prev.cpuSecs) / dt * 100
	if pct < 0 {
		return 0
	}
	return pct
}

// readProcCPU parses utime+stime from /proc/self/stat. Returns ok=false on
// non-Linux, read failure, or a malformed line (Carmack must-fix #6 parallel
// in perf_io.go — never publish a phantom-zero sample).
//
// /proc/self/stat fields 14 (utime) and 15 (stime) are 1-indexed AFTER the
// comm field. comm is parenthesized and may itself contain spaces or ')', so
// we split on the LAST ')' before counting space-separated fields.
func readProcCPU() (cpuSample, bool) {
	if runtime.GOOS != "linux" {
		return cpuSample{}, false
	}
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return cpuSample{}, false
	}
	line := string(data)
	rp := strings.LastIndexByte(line, ')')
	if rp < 0 || rp+2 >= len(line) {
		return cpuSample{}, false
	}
	// Fields after comm: state(0) ppid(1) ... utime is field index 11,
	// stime is field index 12 (0-based, counting from the field after ')').
	fields := strings.Fields(line[rp+2:])
	if len(fields) < 13 {
		return cpuSample{}, false
	}
	utime, err1 := strconv.ParseInt(fields[11], 10, 64)
	stime, err2 := strconv.ParseInt(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return cpuSample{}, false
	}
	return cpuSample{
		at:      time.Now(),
		cpuSecs: float64(utime+stime) / clockTicksPerSec,
	}, true
}
