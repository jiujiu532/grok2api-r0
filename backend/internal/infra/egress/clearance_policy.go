package egress

import (
	"strings"
	"time"
)

const (
	ClearanceModeNone         = "none"
	ClearanceModeManual       = "manual"
	ClearanceModeFlareSolverr = "flaresolverr"

	DefaultAntiBotCooldown  = 45 * time.Second
	DefaultClearanceTimeout = 60 * time.Second
	DefaultClearanceRefresh = time.Hour
)

// ClearancePolicy 描述全局 Cloudflare clearance 与反爬相关运行策略。
type ClearancePolicy struct {
	Mode               string
	CFCookies          string
	UserAgent          string
	FlareSolverrURL    string
	Timeout            time.Duration
	RefreshInterval    time.Duration
	ClientHintsEnabled bool
	AntiBotCooldown    time.Duration
}

func DefaultClearancePolicy() ClearancePolicy {
	return ClearancePolicy{
		Mode:               ClearanceModeNone,
		UserAgent:          DefaultUserAgent,
		Timeout:            DefaultClearanceTimeout,
		RefreshInterval:    DefaultClearanceRefresh,
		ClientHintsEnabled: true,
		AntiBotCooldown:    DefaultAntiBotCooldown,
	}
}

func NormalizeClearanceMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ClearanceModeManual:
		return ClearanceModeManual
	case ClearanceModeFlareSolverr:
		return ClearanceModeFlareSolverr
	default:
		return ClearanceModeNone
	}
}
