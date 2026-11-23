package main

// Tropospheric ducting overlay: shared types (the contract between the
// physics, the config, the Open-Meteo fetcher/refresher and the frontend).
//
// Data flow:
//   config `ducting` (DuctingConfig, off by default)
//     -> grid cells around the mesh's nodes (ducting_grid.go)
//     -> Open-Meteo pressure-level forecast per cell (ducting_source.go)
//     -> per cell and hour: DuctProfile -> AnalyzeDucting (ducting_calc.go)
//     -> in-memory snapshot, served read-only as GET /api/ducting
//        (DuctingResponse). Nothing is written to any database.
//
// Physics (ducting_calc.go):
//   e  = RH/100 * 6.1121 * exp(17.502*t / (240.97 + t))     hPa, t in °C
//   N  = 77.6/T * (P + 4810*e/T)                            ITU-R P.453, P,e hPa, T K
//   M  = N + 0.157*h                                        h metres above ground
//   trapping layer: consecutive levels where M decreases with height
//   duct thickness d (m) and strength ΔM (M units) -> lowest trapped frequency
//   fmin = 120000 / (d * sqrt(ΔM))  MHz   (λmax ≈ 0.0025 * d * sqrt(ΔM) m)

// Duct classes (DuctResult.Class, DuctingCell.C values).
const (
	DuctClassNoData  uint8 = 0 // no 2 m surface point, fewer than 2 usable levels, or no forecast
	DuctClassNormal  uint8 = 1 // min dN/dh > -79 N/km
	DuctClassSuper   uint8 = 2 // super-refractive: min dN/dh <= -79 N/km, no trapping layer
	DuctClassElev    uint8 = 3 // elevated duct (duct bottom above the surface point)
	DuctClassSurface uint8 = 4 // surface-based duct (duct reaches the surface point)
)

// Analysis limits.
const (
	ductMaxHeightAGL    = 3000.0 // m: only the lowest 3 km are analysed
	ductSuperGradientNk = -79.0  // N/km: super-refraction threshold
	ductSurfaceHeightM  = 2.0    // m AGL of the 2 m surface point
	// ductMinLevelGapM: a pressure level less than this far above the 2 m
	// point is ignored. The 2 m values and a pressure level are two
	// different model products, and the level's height above ground is its
	// geopotential height minus the API's terrain elevation, neither exact
	// to a few metres. They differ by a couple of N units whatever the
	// weather, which over a few metres reads as hundreds of N/km: surface
	// 1001 hPa, 12 °C, 95 % under 1000 hPa at 8 m, 13 °C, 88 % is 2.3 N
	// units over 6 m, -385 N/km, a "surface-based duct" trapping only above
	// ~12.8 GHz. Over 30 m the same difference is about -77 N/km and no
	// longer makes a duct on its own.
	ductMinLevelGapM = 30.0
	// ductFminConstMHz: fmin = ductFminConstMHz / (d * sqrt(ΔM)) MHz, the
	// same constant for surface-based and elevated ducts, on purpose. There
	// is no single ratio between the two: ITU-R P.834-4 (2003) section 7.2
	// and its Fig. 4 show that for an elevated duct fmin depends on the
	// shape of the profile below the layer, and is higher than for the
	// equivalent surface duct when the lapse rate is only just past ducting
	// but lower for very intense gradients. NTIA Report 81-69 (Dougherty and
	// Dutton, 1981, eq. 7) likewise applies one thickness law to elevated
	// ducts. With model levels 200-250 m apart, d is itself only a
	// coarse bound, so a per-class factor would add precision the data does
	// not have. d for an elevated duct is the full duct thickness (from
	// where M below the layer equals M at its top), not just the layer.
	ductFminConstMHz = 120000.0
	ductMeanLayerM   = 1500.0 // m: depth of the layer MeanGradNkm averages over
)

// DuctLevel is one point of a vertical profile, ascending by HeightAGL. The
// first level of a profile is always the 2 m surface point.
type DuctLevel struct {
	HeightAGL   float64 // metres above ground
	PressureHPa float64
	TempC       float64
	RHPct       float64 // 0..100
}

// DuctResult is the analysis of one profile (one cell, one hour).
// Heights are metres above ground; all zero when there is no duct.
type DuctResult struct {
	Class      uint8
	FminMHz    float64 // lowest frequency the strongest duct can trap; 0 = no duct
	BaseM      float64 // duct bottom (0 for surface-based)
	TopM       float64 // top of the trapping layer (M minimum)
	DeltaM     float64 // duct strength, M units
	MinGradNkm float64 // most negative dN/dh between consecutive levels, N/km
	// MeanGradNkm is the mean refractivity gradient over the lowest
	// ductMeanLayerM above ground: (N at ductMeanLayerM - N at the 2 m
	// surface point) / ductMeanLayerM, in N/km (ITU-R P.453 style). The map
	// field is coloured by it: averaged over the layer that matters for
	// propagation, it is far less noisy than MinGradNkm, which a single thin
	// layer can dominate. On high terrain, where the profile stops below
	// ductMeanLayerM but its top is at least 1000 m above ground
	// (ductMeanMinLayerM), it is the mean over that shallower depth:
	// (N at the top - N at 2 m) / the top's height. 0 when the profile
	// is shallower than 1000 m (or there is no data).
	MeanGradNkm float64
}

// DuctingModel describes one allowed weather model. Regional models must be
// explicit opt-in: they are never the default, and RegionalNote says where
// they have data.
type DuctingModel struct {
	ID           string    // config value: "gfs", "icon", "rrfs-conus"
	OpenMeteoID  string    // Open-Meteo `models=` value
	Label        string    // shown in the UI
	LevelsHPa    []float64 // pressure levels requested (lowest ~3 km)
	Global       bool
	RegionalNote string // "" for global models
	// Bounds where a regional model has data: south, west, north, east.
	Bounds *[4]float64
}

// DuctingConfig is the `ducting` config section (config.json). Zero values
// mean "default"; see (*DuctingConfig).resolved() in ducting_config.go.
type DuctingConfig struct {
	Enabled        bool        `json:"enabled"`
	Model          string      `json:"model,omitempty"`          // default "gfs"
	Area           *[4]float64 `json:"area,omitempty"`           // [south, west, north, east]; nil = bounding box of the nodes + marginDeg
	MarginDeg      *float64    `json:"marginDeg,omitempty"`      // default 5, allowed 0..10; 0 = just the nodes' bounding box; ignored with area
	GridStepDeg    float64     `json:"gridStepDeg,omitempty"`    // default 0.5, allowed 0.25..2
	MaxPoints      int         `json:"maxPoints,omitempty"`      // default 450, allowed 1..1000
	RefreshMinutes int         `json:"refreshMinutes,omitempty"` // default 360, allowed 60..1440
	ForecastHours  int         `json:"forecastHours,omitempty"`  // default 48, allowed 1..120
	BaseURL        string      `json:"baseUrl,omitempty"`        // default https://api.open-meteo.com
}

// DuctingClientConfig is the `ducting` field of /api/config/client; nil
// (omitted) when the feature is off. Never carries baseUrl.
type DuctingClientConfig struct {
	Model       string  `json:"model"`
	ModelLabel  string  `json:"modelLabel"`
	StepDeg     float64 `json:"stepDeg"`
	Attribution string  `json:"attribution"`
}

// DuctingCell is one grid cell in GET /api/ducting. Every array has one entry
// per DuctingResponse.Times entry. Integers keep the payload small.
type DuctingCell struct {
	Lat float64 `json:"lat"` // cell centre
	Lon float64 `json:"lon"`
	C   []int   `json:"c"`  // class (DuctClass*); []int, not []uint8, which encodes as base64
	F   []int   `json:"f"`  // fmin MHz, rounded; 0 = no duct
	B   []int   `json:"b"`  // duct base m AGL, rounded
	T   []int   `json:"t"`  // duct top m AGL, rounded
	DM  []int   `json:"dm"` // ΔM x10, rounded
	G   []int   `json:"g"`  // min dN/dh N/km, rounded
	N   []int   `json:"n"`  // mean dN/dh over the lowest 1.5 km (high terrain: down to 1 km), N/km, rounded; 0 = no data
}

// DuctingResponse is GET /api/ducting.
type DuctingResponse struct {
	Status      string        `json:"status"`          // "ok" | "pending" (no data yet) | "error"
	Error       string        `json:"error,omitempty"` // short, operator-safe; never a URL
	Model       string        `json:"model"`
	ModelLabel  string        `json:"modelLabel"`
	Attribution string        `json:"attribution"`
	GeneratedAt int64         `json:"generatedAt"` // unix seconds of the last successful refresh; 0 if none
	StepDeg     float64       `json:"stepDeg"`
	Times       []int64       `json:"times"` // unix seconds, hourly, ascending
	Cells       []DuctingCell `json:"cells"`
}
