package main

import (
	"bufio"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/dbschema"
)

// Set via -ldflags at build time
var Version string
var Commit string
var BuildTime string

func resolveCommit() string {
	if Commit != "" {
		return Commit
	}

	// Try .git-commit file (baked by Docker / CI)
	if data, err := os.ReadFile(".git-commit"); err == nil {
		if c := strings.TrimSpace(string(data)); c != "" && c != "unknown" {
			return c
		}
	}

	// Try git rev-parse at runtime
	if out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}

	return "unknown"
}

func resolveVersion() string {
	if Version != "" {
		return Version
	}
	return "unknown"
}

func resolveBuildTime() string {
	if BuildTime != "" {
		return BuildTime
	}
	return "unknown"
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
	}
	return hj.Hijack()
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Push(target string, opts *http.PushOptions) error {
	if p, ok := r.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

func accessLogMiddleware(next http.Handler) http.Handler {
	_ = os.MkdirAll("/app/logs", 0o755)

	f, err := os.OpenFile("/app/logs/access.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("[accesslog] failed to open /app/logs/access.log: %v", err)
		return next
	}

	logger := log.New(io.MultiWriter(f), "", 0)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(rec, r)

		remoteAddr := r.Header.Get("X-Forwarded-For")
		if remoteAddr == "" {
			remoteAddr = r.RemoteAddr
		}

		logger.Printf("%s - - [%s] \"%s %s %s\" %d %d \"%s\" \"%s\" %dms",
			remoteAddr,
			time.Now().Format("02/Jan/2006:15:04:05 -0700"),
			r.Method,
			r.URL.RequestURI(),
			r.Proto,
			rec.status,
			rec.bytes,
			r.Referer(),
			r.UserAgent(),
			time.Since(start).Milliseconds(),
		)
	})
}

func main() {
	// pprof profiling — off by default, enable with ENABLE_PPROF=true
	if os.Getenv("ENABLE_PPROF") == "true" {
		pprofPort := os.Getenv("PPROF_PORT")
		if pprofPort == "" {
			pprofPort = "6060"
		}
		// Bind to loopback only — the pprof endpoints expose heap/goroutine
		// dumps and must never be reachable from outside the host. Operators
		// who need remote access should tunnel (e.g. SSH port-forward).
		pprofAddr := "127.0.0.1:" + pprofPort
		go func() {
			log.Printf("[pprof] profiling UI at http://%s/debug/pprof/", pprofAddr)
			if err := http.ListenAndServe(pprofAddr, nil); err != nil {
				log.Printf("[pprof] failed to start: %v (non-fatal)", err)
			}
		}()
	}

	var (
		configDir string
		port      int
		dbPath    string
		publicDir string
		pollMs    int
	)

	flag.StringVar(&configDir, "config-dir", ".", "Directory containing config.json")
	flag.IntVar(&port, "port", 0, "HTTP port (overrides config)")
	flag.StringVar(&dbPath, "db", "", "SQLite database path (overrides config/env)")
	flag.StringVar(&publicDir, "public", "public", "Directory to serve static files from")
	flag.IntVar(&pollMs, "poll-ms", 1000, "SQLite poll interval for WebSocket broadcast (ms)")
	flag.Parse()

	// Load config
	cfg, err := LoadConfig(configDir)
	if err != nil {
		log.Printf("[config] warning: %v (using defaults)", err)
	}

	// CLI flags override config
	if port > 0 {
		cfg.Port = port
	}
	if cfg.Port == 0 {
		cfg.Port = 3000
	}
	if dbPath != "" {
		cfg.DBPath = dbPath
	}

	if cfg.APIKey == "" {
		log.Printf("[security] WARNING: no apiKey configured — write endpoints are BLOCKED (set apiKey in config.json to enable them)")
	} else if IsWeakAPIKey(cfg.APIKey) {
		log.Printf("[security] WARNING: API key is weak or a known default — write endpoints are vulnerable")
	}

	// Apply Go runtime soft memory limit (#836).
	// Honors GOMEMLIMIT if set; otherwise derives from packetStore.maxMemoryMB.
	{
		_, envSet := os.LookupEnv("GOMEMLIMIT")
		maxMB := 0
		if cfg.PacketStore != nil {
			maxMB = cfg.PacketStore.MaxMemoryMB
		}
		limit, source := applyMemoryLimit(maxMB, envSet)
		switch source {
		case "env":
			log.Printf("[memlimit] using GOMEMLIMIT from environment (%s)", os.Getenv("GOMEMLIMIT"))
		case "derived":
			log.Printf("[memlimit] derived from packetStore.maxMemoryMB=%d → %d MiB (1.5x headroom)", maxMB, limit/(1024*1024))
		default:
			log.Printf("[memlimit] no soft memory limit set (GOMEMLIMIT unset, packetStore.maxMemoryMB=0); recommend setting one to avoid container OOM-kill")
		}
	}

	// Resolve DB path
	resolvedDB := cfg.ResolveDBPath(configDir)
	log.Printf("[config] port=%d db=%s public=%s", cfg.Port, resolvedDB, publicDir)

	if len(cfg.NodeBlacklist) > 0 {
		log.Printf("[config] nodeBlacklist: %d node(s) will be hidden from API", len(cfg.NodeBlacklist))
		for _, pk := range cfg.NodeBlacklist {
			if trimmed := strings.ToLower(strings.TrimSpace(pk)); trimmed != "" {
				log.Printf("[config] blacklisted: %s", trimmed)
			}
		}
	}

	// Open database
	database, err := OpenDB(resolvedDB)
	if err != nil {
		log.Fatalf("[db] failed to open %s: %v", resolvedDB, err)
	}

	var dbCloseOnce sync.Once
	dbClose := func() error {
		var err error
		dbCloseOnce.Do(func() {
			err = database.Close()
		})
		return err
	}
	defer dbClose()

	// Verify DB has expected tables
	var tableName string
	err = database.conn.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='transmissions'").Scan(&tableName)
	if err == sql.ErrNoRows {
		log.Fatalf("[db] table 'transmissions' not found — is this a Cornmeister.nl database?")
	}

	stats, err := database.GetStats()
	if err != nil {
		log.Printf("[db] warning: could not read stats: %v", err)
	} else {
		log.Printf("[db] transmissions=%d observations=%d nodes=%d observers=%d",
			stats.TotalTransmissions, stats.TotalObservations, stats.TotalNodes, stats.TotalObservers)
	}

	// auto_vacuum is checked + migrated by the ingestor (#1283). The
	// server is read-only and must not race the writer for the lock.

	// Assert all schema migrations the ingestor owns have already run
	// (#1287). The server NEVER migrates — it only reads. If a required
	// column/index/table is missing, the operator must restart the
	// ingestor (which owns dbschema.Apply) before this server can start.
	if err := dbschema.AssertReady(database.conn); err != nil {
		log.Fatalf("[db] schema not ready (ingestor must run migrations first): %v", err)
	}

	// In-memory packet store
	store := NewPacketStore(database, cfg.PacketStore, cfg.CacheTTL)
	store.config = cfg
	if err := store.Load(); err != nil {
		log.Fatalf("[store] failed to load: %v", err)
	}
	if store.hotStartupHours > 0 {
		log.Printf("[store] starting background load: filling retentionHours=%gh from hotStartupHours=%gh",
			store.retentionHours, store.hotStartupHours)
		go store.loadBackgroundChunks()
	}

	// Initialize persisted neighbor graph.
	// Per #1287, schema migrations all live in the ingestor (see
	// dbschema.Apply). The server merely loads the snapshot here and
	// then refreshes it via the recompNeighborGraph slot every 60s.
	dbPath = database.path
	database.hasResolvedPath = true // dbschema.AssertReady above already verified observations.resolved_path exists

	// WaitGroup for background init steps that gate /api/healthz readiness.
	var initWg sync.WaitGroup

	// Load or build neighbor graph
	if neighborEdgesTableExists(database.conn) {
		store.graph.Store(loadNeighborEdgesFromDB(database.conn))
		log.Printf("[neighbor] loaded persisted neighbor graph")
	} else {
		// No persisted snapshot yet (e.g. fresh DB before the ingestor
		// has run its first edge-build cycle). Build an in-memory graph
		// from the packets we already have so reads aren't empty. We
		// do NOT persist — the ingestor owns neighbor_edges writes per
		// #1287; the recompNeighborGraph recomputer will pick up the
		// real snapshot as soon as the ingestor populates it.
		log.Printf("[neighbor] no persisted edges found, will build in-memory in background...")
		store.graph.Store(NewNeighborGraph())
		initWg.Add(1)
		go func() {
			defer initWg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[neighbor] graph build panic recovered: %v", r)
				}
			}()
			built := BuildFromStore(store)
			store.graph.Store(built)
			log.Printf("[neighbor] in-memory graph build complete")
		}()
	}

	// Initial pickBestObservation runs in background — doesn't need to block HTTP.
	// API serves best-effort data until this completes (~10s for 100K txs).
	// Processes in chunks of 5000, releasing the lock between chunks so API
	// handlers remain responsive.
	initWg.Add(1)
	go func() {
		defer initWg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[store] pickBestObservation panic recovered: %v", r)
			}
		}()

		const chunkSize = 5000

		store.mu.RLock()
		totalPackets := len(store.packets)
		store.mu.RUnlock()

		for i := 0; i < totalPackets; i += chunkSize {
			end := i + chunkSize
			if end > totalPackets {
				end = totalPackets
			}

			store.mu.Lock()
			for j := i; j < end && j < len(store.packets); j++ {
				pickBestObservation(store.packets[j])
			}
			store.mu.Unlock()

			if end < totalPackets {
				time.Sleep(10 * time.Millisecond) // yield to API handlers
			}
		}

		log.Printf("[store] initial pickBestObservation complete (%d transmissions)", totalPackets)
	}()

	// Mark server ready once all background init completes.
	go func() {
		initWg.Wait()
		readiness.Store(1)
		log.Printf("[server] readiness: ready=true (background init complete)")
	}()

	// WebSocket hub
	hub := NewHub()
	// Validate WebSocket upgrade Origin against the same allowlist used for
	// CORS — prevents cross-site WebSocket hijacking (same-origin still works).
	hub.allowedOrigins = cfg.CORSAllowedOrigins
	hub.upgrader.EnableCompression = cfg.WSCompressionEnabled()

	// HTTP server
	srv := NewServer(database, cfg, hub)
	srv.configDir = configDir
	srv.store = store
	srv.channelKeys = loadServerChannelKeys(cfg, configDir)

	// Perf-history persistence: load existing samples from the JSON sidecar
	// so the /api/perf/history graphs survive server restarts.
	perfHistPath := perfHistoryFilePath(resolvedDB)
	srv.perfHistoryPath = perfHistPath
	if samples := loadPerfHistoryFromFile(perfHistPath); len(samples) > 0 {
		srv.perfHistoryMu.Lock()
		srv.perfHistory = samples
		srv.perfHistoryMu.Unlock()
		log.Printf("[perf-history] loaded %d samples from %s", len(samples), perfHistPath)
	}

	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	// WebSocket endpoint
	router.HandleFunc("/ws", hub.ServeWS)

	// Static files + SPA fallback
	absPublic, _ := filepath.Abs(publicDir)
	srv.publicDir = absPublic
	if _, err := os.Stat(absPublic); err == nil {
		fs := http.FileServer(http.Dir(absPublic))
		router.PathPrefix("/").Handler(wsOrStatic(hub, srv.spaHandler(absPublic, fs)))
		log.Printf("[static] serving %s", absPublic)
	} else {
		log.Printf("[static] directory %s not found — API-only mode", absPublic)
		router.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`
# Cornmeister.nl

Frontend not found. API available at /api/
`))
		})
	}

	// Start SQLite poller for WebSocket broadcast
	poller := NewPoller(database, hub, time.Duration(pollMs)*time.Millisecond)
	poller.store = store
	go poller.Start()

	// Start periodic eviction
	stopEviction := store.StartEvictionTicker()
	defer stopEviction()

	// Perf-history collector — snapshots metrics every minute into the ring
	// buffer and flushes to the JSON sidecar on shutdown.
	stopPerfHistory := srv.startPerfHistoryCollector()
	defer stopPerfHistory()

	// Steady-state analytics recomputers (issue #1240). Replaces the
	// on-request compute-then-cache pattern for the default (region="",
	// zero-window) analytics queries with a background refresh loop so
	// reads always hit cache in <1ms.
	stopAnalyticsRecomp := store.StartAnalyticsRecomputers(
		cfg.AnalyticsDefaultRecomputeInterval(),
		cfg.AnalyticsRecomputeIntervals(),
	)
	defer stopAnalyticsRecomp()
	log.Printf("[analytics-recompute] background recompute enabled (default=%s)", cfg.AnalyticsDefaultRecomputeInterval())

	// Steady-state repeater-enrichment recomputer (issue #1262).
	// Prewarms the bulk caches feeding handleNodes so the very first
	// /api/nodes?limit=2000 from live.js's SPA bootstrap hits a
	// populated cache instead of paying a 15.7s on-thread rebuild.
	// Uses the configured RelayActiveHours window and the same
	// default recompute interval as the other analytics caches.
	relayWindowHours := cfg.GetHealthThresholds().RelayActiveHours
	stopRepeaterEnrichRecomp := store.StartRepeaterEnrichmentRecomputer(
		relayWindowHours,
		cfg.AnalyticsDefaultRecomputeInterval(),
	)
	defer stopRepeaterEnrichRecomp()
	log.Printf("[repeater-enrich-recompute] background recompute enabled (window=%.1fh, interval=%s)",
		relayWindowHours, cfg.AnalyticsDefaultRecomputeInterval())

	// Steady-state bridge-centrality recomputer (issue #672 axis 2).
	// Computes betweenness centrality over the in-memory neighbor
	// graph and stores the per-pubkey score map atomically. Read by
	// handleNodes via a single atomic load.
	stopBridgeRecomp := store.StartBridgeScoreRecomputer(
		cfg.AnalyticsDefaultRecomputeInterval(),
	)
	defer stopBridgeRecomp()
	log.Printf("[bridge-recompute] background recompute enabled (interval=%s)",
		cfg.AnalyticsDefaultRecomputeInterval())

	// Steady-state neighbor-graph snapshot recomputer (issue #1287).
	// Per Option 4: the ingestor owns neighbor_edges; the server
	// READS the snapshot every 60s and atomic-swaps it into s.graph.
	// This is the ONLY path that updates s.graph at steady state.
	stopNeighborRecomp := store.StartNeighborGraphRecomputer(NeighborGraphRecomputerDefaultInterval)
	defer stopNeighborRecomp()
	log.Printf("[neighbor-recompute] snapshot reload enabled (interval=%s)",
		NeighborGraphRecomputerDefaultInterval)

	// Packet / metrics / observer retention moved to the ingestor in
	// #1283 (writes only belong on the writer process). Neighbor-edge
	// pruning moved to the ingestor in #1287 for the same reason. The
	// server no longer schedules any of these; the ingestor's tickers
	// handle them.
	_ = cfg.IncrementalVacuumPages() // kept reachable for config validation; not used here
	_ = cfg.NeighborMaxAgeDays()     // ditto — owned by ingestor now

	// Graceful shutdown
	var handler http.Handler = accessLogMiddleware(router)
	if cfg.GZipEnabled() {
		handler = gzipMiddlewareWithConfig(cfg.Compression, router)
		log.Printf("[server] HTTP gzip compression enabled")
	}
	if cfg.WSCompressionEnabled() {
		log.Printf("[server] WebSocket permessage-deflate compression enabled")
	}
	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		log.Printf("[server] received %v, shutting down...", sig)

		// 1. Stop accepting new WebSocket/poll data
		poller.Stop()

		// 1b. Auto-prune tickers were all relocated to the ingestor in
		// #1283/#1287 — nothing to stop here.

		// 1c. Stop steady-state analytics recomputers (issue #1240).
		// Must happen before dbClose so any in-flight compute that
		// reaches into SQLite has finished.
		if stopAnalyticsRecomp != nil {
			stopAnalyticsRecomp()
		}

		// 2. Gracefully drain HTTP connections (up to 15s)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("[server] HTTP shutdown error: %v", err)
		}

		// 3. Close WebSocket hub
		hub.Close()

		// 4. Close database (release SQLite WAL lock)
		if err := dbClose(); err != nil {
			log.Printf("[server] DB close error: %v", err)
		}

		log.Println("[server] shutdown complete")
	}()

	log.Printf("[server] Cornmeister.nl (Go) listening on http://localhost:%d", cfg.Port)

	// Backfills (resolved_path, from_pubkey) moved to the ingestor in
	// #1287 — they are write operations and belong on the writer
	// process. The server reads the results via the periodic
	// recompNeighborGraph / fetchResolvedPathForObs paths.

	// Migrate old content hashes in background (one-time, idempotent).
	go migrateContentHashesAsync(store, 5000, 100*time.Millisecond)

	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("[server] %v", err)
	}
}

// themeCSSMap mirrors THEME_CSS_MAP in public/customize-v2.js — a theme config
// key → CSS custom property. Used to inline the server theme into index.html so
// the page paints correct colors before the JS theme pipeline runs.
var themeCSSMap = [][2]string{
	{"accent", "--accent"}, {"accentHover", "--accent-hover"},
	{"navBg", "--nav-bg"}, {"navBg2", "--nav-bg2"}, {"navText", "--nav-text"}, {"navTextMuted", "--nav-text-muted"},
	{"background", "--surface-0"}, {"text", "--text"}, {"textMuted", "--text-muted"}, {"border", "--border"},
	{"statusGreen", "--status-green"}, {"statusYellow", "--status-yellow"}, {"statusRed", "--status-red"},
	{"surface1", "--surface-1"}, {"surface2", "--surface-2"}, {"surface3", "--surface-3"},
	{"sectionBg", "--section-bg"},
	{"cardBg", "--card-bg"}, {"contentBg", "--content-bg"}, {"detailBg", "--detail-bg"},
	{"inputBg", "--input-bg"}, {"rowStripe", "--row-stripe"}, {"rowHover", "--row-hover"}, {"selectedBg", "--selected-bg"},
	{"font", "--font"}, {"mono", "--mono"},
}

// themeVarsBlock emits "--var:value;" declarations for a theme map, skipping
// values containing characters that could break out of the <style> context.
func themeVarsBlock(theme map[string]interface{}) string {
	var b strings.Builder
	for _, kv := range themeCSSMap {
		raw, ok := theme[kv[0]]
		if !ok {
			continue
		}
		val, ok := raw.(string)
		if !ok || val == "" || strings.ContainsAny(val, "<>{};\n\r") {
			continue
		}
		b.WriteString(kv[1])
		b.WriteByte(':')
		b.WriteString(val)
		b.WriteByte(';')
	}
	return b.String()
}

// buildThemeStyleTag renders an inline <style> block setting the server theme's
// CSS variables for light, manual-dark, and OS-dark scopes — matching the
// cascade in style.css and isDarkMode() in customize-v2.js. This eliminates the
// color flash that occurred while app.js fetched /api/config/theme.
func buildThemeStyleTag(tr ThemeResponse) string {
	light := themeVarsBlock(tr.Theme)
	darkMerged := map[string]interface{}{}
	for k, v := range tr.Theme {
		darkMerged[k] = v
	}
	for k, v := range tr.ThemeDark {
		darkMerged[k] = v
	}
	dark := themeVarsBlock(darkMerged)
	if light == "" && dark == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<style id="server-theme">`)
	if light != "" {
		b.WriteString(":root{" + light + "}")
	}
	if dark != "" {
		b.WriteString(`[data-theme="dark"]{` + dark + "}")
		b.WriteString(`@media (prefers-color-scheme:dark){:root:not([data-theme="light"]):not([data-theme="dark"]){` + dark + "}}")
	}
	b.WriteString("</style>")
	return b.String()
}

// metaStr safely reads a string value from a meta map for embedding in HTML.
// It HTML-escapes the value (via html.EscapeString) so it cannot break out of
// a tag or double-quoted attribute context. Absent keys, non-strings, empty
// strings, or values containing newlines fall back to the supplied default.
func metaStr(m map[string]interface{}, key, fallback string) string {
	raw, ok := m[key]
	if !ok {
		return fallback
	}
	s, ok := raw.(string)
	if !ok || s == "" || strings.ContainsAny(s, "\n\r") {
		return fallback
	}
	return html.EscapeString(s)
}

// buildSiteMetaTag renders the <title> and OpenGraph/Twitter meta tags from the
// active template/config meta map. Replaces the static __SITE_META__ placeholder
// in index.html so social crawlers see template-specific values.
func buildSiteMetaTag(tr ThemeResponse) string {
	m := tr.Meta
	if m == nil {
		m = map[string]interface{}{}
	}
	title := metaStr(m, "title", "CoreScope-EVO")
	desc := metaStr(m, "description", "Real-time MeshCore LoRa mesh network analyzer")
	ogImage := metaStr(m, "ogImage", "")
	ogURL := metaStr(m, "ogUrl", "")
	themeColor := metaStr(m, "themeColor", "#0a0a0a")
	var b strings.Builder
	b.WriteString("<title>" + title + "</title>")
	b.WriteString(`<meta name="description" content="` + desc + `">`)
	b.WriteString(`<meta property="og:title" content="` + title + `">`)
	b.WriteString(`<meta property="og:description" content="` + desc + `">`)
	if ogImage != "" {
		b.WriteString(`<meta property="og:image" content="` + ogImage + `">`)
		b.WriteString(`<meta name="twitter:image" content="` + ogImage + `">`)
	}
	if ogURL != "" {
		b.WriteString(`<meta property="og:url" content="` + ogURL + `">`)
	}
	b.WriteString(`<meta property="og:type" content="website">`)
	b.WriteString(`<meta name="twitter:card" content="summary_large_image">`)
	b.WriteString(`<meta name="twitter:title" content="` + title + `">`)
	b.WriteString(`<meta name="twitter:description" content="` + desc + `">`)
	b.WriteString(`<meta name="theme-color" content="` + themeColor + `">`)
	return b.String()
}

// defaultNavLinks is the built-in top-nav markup, used when the active
// template does not define its own `nav` array. Kept byte-identical to the
// historical hardcoded nav so non-templated deploys are unchanged.
const defaultNavLinks = `<a href="#/home" class="nav-link" data-route="home" data-priority="high">Home</a>
        <a href="#/packets" class="nav-link" data-route="packets" data-priority="high">Packets</a>
        <a href="#/map" class="nav-link" data-route="map" data-priority="high">Map</a>
        <a href="#/live" class="nav-link" data-route="live" data-priority="high">🔴 Live</a>
        <a href="#/channels" class="nav-link" data-route="channels">Channels</a>
        <a href="#/nodes" class="nav-link" data-route="nodes" data-priority="high">Nodes</a>
        <a href="#/tools" class="nav-link" data-route="tools">🛠️ Tools</a>
        <a href="#/observers" class="nav-link" data-route="observers">Observers</a>
        <a href="#/analytics" class="nav-link" data-route="analytics">Analytics</a>
        <a href="#/audio-lab" class="nav-link" data-route="audio-lab">🎵 Lab</a>
        <a href="#/perf" class="nav-link" data-route="perf" data-priority="high">⚡ Perf</a>`

// buildNavLinks renders the .nav-link anchors for the top nav. When the active
// template supplies a `nav` array each entry {route, hash, label, priority?}
// becomes an anchor; otherwise the built-in defaultNavLinks is used. Replaces
// the __NAV_LINKS__ placeholder in index.html so the nav is correct on first
// paint with no client-side reflow.
func buildNavLinks(tr ThemeResponse) string {
	if len(tr.Nav) == 0 {
		return defaultNavLinks
	}
	var b strings.Builder
	n := 0
	for _, item := range tr.Nav {
		hash := metaStr(item, "hash", "")
		label := metaStr(item, "label", "")
		if hash == "" || label == "" {
			continue
		}
		if n > 0 {
			b.WriteString("\n        ")
		}
		n++
		b.WriteString(`<a href="` + html.EscapeString(hash) + `" class="nav-link"`)
		if route := metaStr(item, "route", ""); route != "" {
			b.WriteString(` data-route="` + html.EscapeString(route) + `"`)
		}
		if priority := metaStr(item, "priority", ""); priority != "" {
			b.WriteString(` data-priority="` + html.EscapeString(priority) + `"`)
		}
		b.WriteString(`>` + html.EscapeString(label) + `</a>`)
	}
	if n == 0 {
		return defaultNavLinks
	}
	return b.String()
}

// spaHandler serves static files, falling back to index.html for SPA routes.
// It reads index.html once at creation time and replaces the __BUST__ placeholder
// with a Unix timestamp so browsers fetch fresh JS/CSS after each server restart,
// and the __THEME_STYLE__ placeholder with the inlined server theme.
func (s *Server) spaHandler(root string, fs http.Handler) http.Handler {
	// Pre-process index.html: replace __BUST__ with a cache-bust timestamp
	indexPath := filepath.Join(root, "index.html")
	rawHTML, err := os.ReadFile(indexPath)
	if err != nil {
		log.Printf("[static] warning: could not read index.html for cache-bust: %v", err)
		rawHTML = []byte(`
# Cornmeister.nl

index.html not found
`)
	}
	bustValue := fmt.Sprintf("%d", time.Now().Unix())
	tr := s.buildThemeResponse()
	processed := strings.ReplaceAll(string(rawHTML), "__BUST__", bustValue)
	processed = strings.ReplaceAll(processed, "__THEME_STYLE__", buildThemeStyleTag(tr))
	processed = strings.ReplaceAll(processed, "__SITE_META__", buildSiteMetaTag(tr))
	processed = strings.ReplaceAll(processed, "__NAV_LINKS__", buildNavLinks(tr))
	indexHTML := []byte(processed)
	log.Printf("[static] cache-bust value: %s", bustValue)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve pre-processed index.html for root and /index.html
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			_, _ = w.Write(indexHTML)
			return
		}

		path := filepath.Join(root, r.URL.Path)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			// SPA fallback — serve pre-processed index.html
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			_, _ = w.Write(indexHTML)
			return
		}

		// Cache strategy: index.html references every JS/CSS with ?v=<bustValue>
		// (set above), so versioned asset URLs are content-addressed for the
		// lifetime of a server build. We can mark them immutable.
		// HTML stays no-cache so deploys take effect immediately.
		ext := filepath.Ext(path)
		if ext == ".html" {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		} else if r.URL.Query().Get("v") != "" {
			// Versioned asset — safe to cache forever.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else if ext == ".js" || ext == ".css" || ext == ".woff" || ext == ".woff2" ||
			ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" ||
			ext == ".svg" || ext == ".ico" || ext == ".webp" {
			// Unversioned static asset — cache for an hour so CDN/browsers can
			// serve repeat visits, but redeploys aren't blocked for long.
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}

		fs.ServeHTTP(w, r)
	})
}
