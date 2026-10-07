package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/meshcore-analyzer/users"
)

// UserManagementConfig is the "userManagement" block of config.json.
type UserManagementConfig struct {
	Enabled          bool                    `json:"enabled"`
	DBPath           string                  `json:"dbPath,omitempty"`
	AdminEmails      []string                `json:"adminEmails,omitempty"`
	PublicBaseURL    string                  `json:"publicBaseUrl,omitempty"`
	SessionDays      int                     `json:"sessionDays,omitempty"`
	TrustedProxies   []string                `json:"trustedProxies,omitempty"`
	Mail             UserMailConfig          `json:"mail"`
	ChannelProposals *ChannelProposalsConfig `json:"channelProposals,omitempty"`
	Notifications    *NotificationsConfig    `json:"notifications,omitempty"`
}

// UserMailConfig is userManagement.mail.
type UserMailConfig struct {
	Provider      string `json:"provider,omitempty"`
	BrevoAPIKey   string `json:"brevoApiKey,omitempty"`
	FromEmail     string `json:"fromEmail,omitempty"`
	FromName      string `json:"fromName,omitempty"`
	WebhookSecret string `json:"webhookSecret,omitempty"`
}

// ChannelProposalsConfig is userManagement.channelProposals
// (docs/specs/2026-10-07-channel-proposals-design.md). Off by default.
type ChannelProposalsConfig struct {
	Enabled       bool `json:"enabled"`
	MaxPending    int  `json:"maxPending,omitempty"`
	MaxApproved   int  `json:"maxApproved,omitempty"`
	PerUserPerDay int  `json:"perUserPerDay,omitempty"`
}

// proposalSettings is the resolved form; the zero value means off.
type proposalSettings struct {
	enabled       bool
	maxPending    int // pending proposals of every kind
	maxApproved   int // approved hashtag channels; bounds the ingestor's key set
	perUserPerDay int
}

const (
	defaultMaxPendingProposals    = 100
	defaultMaxApprovedChannels    = 128
	defaultProposalsPerUserPerDay = 5
)

func positiveOr(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// resolveProposals fills the defaults for absent, zero or negative limits.
func resolveProposals(c *ChannelProposalsConfig) proposalSettings {
	if c == nil || !c.Enabled {
		return proposalSettings{}
	}
	return proposalSettings{
		enabled:       true,
		maxPending:    positiveOr(c.MaxPending, defaultMaxPendingProposals),
		maxApproved:   positiveOr(c.MaxApproved, defaultMaxApprovedChannels),
		perUserPerDay: positiveOr(c.PerUserPerDay, defaultProposalsPerUserPerDay),
	}
}

// NotificationsConfig is userManagement.notifications
// (docs/specs/2026-10-07-node-notifications-design.md). Off by default.
type NotificationsConfig struct {
	Enabled           bool `json:"enabled"`
	IntervalMinutes   int  `json:"intervalMinutes,omitempty"`
	PerUserPerDay     int  `json:"perUserPerDay,omitempty"`
	MaxMailsPerDay    int  `json:"maxMailsPerDay,omitempty"`
	MaxWatchesPerUser int  `json:"maxWatchesPerUser,omitempty"`
}

// notifySettings is the resolved form; the zero value means off.
type notifySettings struct {
	enabled           bool
	interval          time.Duration // between evaluations, at least a minute
	perUserPerDay     int           // notification mails per user per 24 hours
	maxMailsPerDay    int           // notification mails per instance per 24 hours
	maxWatchesPerUser int
}

const (
	defaultNotifyIntervalMinutes = 5
	defaultNotifyPerUserPerDay   = 20
	// defaultNotifyMaxMailsPerDay counts notification mail only. A third of
	// Brevo's free 300 a day, so activation, reset and address-change mail
	// (and other senders on the same account) still fit in the quota.
	defaultNotifyMaxMailsPerDay = 100
	defaultNotifyMaxWatches     = 50
)

// resolveNotifications fills the defaults for absent, zero or negative
// values. The interval is in whole minutes, so its floor is one minute.
func resolveNotifications(c *NotificationsConfig) notifySettings {
	if c == nil || !c.Enabled {
		return notifySettings{}
	}
	return notifySettings{
		enabled:           true,
		interval:          time.Duration(positiveOr(c.IntervalMinutes, defaultNotifyIntervalMinutes)) * time.Minute,
		perUserPerDay:     positiveOr(c.PerUserPerDay, defaultNotifyPerUserPerDay),
		maxMailsPerDay:    positiveOr(c.MaxMailsPerDay, defaultNotifyMaxMailsPerDay),
		maxWatchesPerUser: positiveOr(c.MaxWatchesPerUser, defaultNotifyMaxWatches),
	}
}

// UserManagementEnabled reports whether optional accounts are on. Nil config
// or absent section means off (the default).
func (c *Config) UserManagementEnabled() bool {
	return c != nil && c.UserManagement != nil && c.UserManagement.Enabled
}

// userMgmtSettings is the validated, resolved form the auth service runs on.
type userMgmtSettings struct {
	dbPath         string
	adminEmails    map[string]bool
	baseURL        *url.URL // no trailing slash, no query or fragment
	secureCookie   bool
	sessionTTL     time.Duration
	trustedProxies []*net.IPNet
	provider       string // "brevo" or (e2etest builds only) "fake"
	brevoAPIKey    string
	fromEmail      string
	fromName       string
	webhookSecret  string
	proposals      proposalSettings
	notify         notifySettings
}

const defaultSessionDays = 30

// fakeMailerAllowed is flipped only by the e2etest build (auth_e2e.go).
var fakeMailerAllowed bool

// resolveUserManagement validates the block and fills defaults. It refuses
// configurations where nobody could activate an account, so the server
// fails at startup instead of running half-working.
func resolveUserManagement(u *UserManagementConfig, measurementDBPath string, getenv func(string) string) (*userMgmtSettings, error) {
	set := &userMgmtSettings{adminEmails: map[string]bool{}}

	set.dbPath = strings.TrimSpace(u.DBPath)
	if set.dbPath == "" {
		set.dbPath = filepath.Join(filepath.Dir(measurementDBPath), "users.db")
	}
	for _, raw := range u.AdminEmails {
		e, err := users.NormalizeEmail(raw)
		if err != nil {
			return nil, fmt.Errorf("userManagement.adminEmails: %q is not a valid address", raw)
		}
		set.adminEmails[e] = true
	}

	base, err := url.Parse(strings.TrimSpace(u.PublicBaseURL))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, errors.New("userManagement.publicBaseUrl must be an absolute http(s) URL, e.g. https://corescope.example.org")
	}
	base.Path = strings.TrimRight(base.Path, "/")
	base.RawQuery, base.Fragment = "", ""
	set.baseURL = base
	set.secureCookie = base.Scheme == "https"

	days := u.SessionDays
	if days <= 0 {
		days = defaultSessionDays
	}
	if days > 365 {
		days = 365
	}
	set.sessionTTL = time.Duration(days) * 24 * time.Hour
	set.trustedProxies = parseCIDRList(u.TrustedProxies, "userManagement.trustedProxies")

	set.provider = strings.ToLower(strings.TrimSpace(u.Mail.Provider))
	if set.provider == "" {
		set.provider = "brevo"
	}
	set.brevoAPIKey = envOrValue(getenv, "CORESCOPE_BREVO_API_KEY", u.Mail.BrevoAPIKey)
	set.webhookSecret = envOrValue(getenv, "CORESCOPE_BREVO_WEBHOOK_SECRET", u.Mail.WebhookSecret)
	from, err := users.NormalizeEmail(u.Mail.FromEmail)
	if err != nil {
		return nil, errors.New("userManagement.mail.fromEmail must be a valid address")
	}
	set.fromEmail = from
	set.fromName = strings.TrimSpace(u.Mail.FromName)
	if set.fromName == "" {
		set.fromName = "CoreScope"
	}

	switch set.provider {
	case "brevo":
		if set.brevoAPIKey == "" {
			return nil, errors.New("userManagement.mail: a Brevo API key is required (mail.brevoApiKey or CORESCOPE_BREVO_API_KEY)")
		}
	case "fake":
		if !fakeMailerAllowed {
			return nil, errors.New(`userManagement.mail.provider "fake" is only available in e2etest builds`)
		}
	default:
		return nil, fmt.Errorf("userManagement.mail.provider %q is not supported (use \"brevo\")", u.Mail.Provider)
	}
	if set.webhookSecret != "" && len(set.webhookSecret) < 16 {
		return nil, errors.New("userManagement.mail.webhookSecret must be at least 16 characters")
	}
	set.proposals = resolveProposals(u.ChannelProposals)
	set.notify = resolveNotifications(u.Notifications)
	return set, nil
}

func envOrValue(getenv func(string) string, key, fallback string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return strings.TrimSpace(fallback)
}
