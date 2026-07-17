package clearance

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	domainegress "github.com/chenyme/grok2api/backend/internal/domain/egress"
	egressapp "github.com/chenyme/grok2api/backend/internal/application/egress"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

// PolicySource 提供当前 clearance 策略。
type PolicySource func() infraegress.ClearancePolicy

// Service 负责 FlareSolverr / 手动 clearance 刷新。
type Service struct {
	mu         sync.Mutex
	repository repository.EgressRepository
	cipher     *security.Cipher
	policy     PolicySource
	client     *http.Client
	logger     *slog.Logger
	lastRun    time.Time
}

func NewService(repository repository.EgressRepository, cipher *security.Cipher, policy PolicySource, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		repository: repository,
		cipher:     cipher,
		policy:     policy,
		client:     &http.Client{Timeout: 90 * time.Second},
		logger:     logger,
	}
}

// RunLoop 按 RefreshInterval 周期刷新 flaresolverr clearance。
func (s *Service) RunLoop(ctx context.Context) error {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := s.maybeRefresh(ctx); err != nil && ctx.Err() == nil {
				s.logger.Warn("clearance_refresh_failed", "error", err)
			}
		}
	}
}

func (s *Service) maybeRefresh(ctx context.Context) error {
	policy := s.currentPolicy()
	if policy.Mode != infraegress.ClearanceModeFlareSolverr {
		return nil
	}
	interval := policy.RefreshInterval
	if interval <= 0 {
		interval = infraegress.DefaultClearanceRefresh
	}
	s.mu.Lock()
	due := s.lastRun.IsZero() || time.Since(s.lastRun) >= interval
	s.mu.Unlock()
	if !due {
		return nil
	}
	result, err := s.RefreshAll(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.lastRun = time.Now().UTC()
	s.mu.Unlock()
	s.logger.Info("clearance_refresh_completed", "updated", result.Updated, "failed", result.Failed, "skipped", result.Skipped)
	return nil
}

// RefreshResult 汇总一次刷新结果。
type RefreshResult struct {
	Updated int `json:"updated"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// RefreshAll 立即按当前策略刷新可用出口节点 clearance。
func (s *Service) RefreshAll(ctx context.Context) (RefreshResult, error) {
	policy := s.currentPolicy()
	switch policy.Mode {
	case infraegress.ClearanceModeNone:
		return RefreshResult{}, fmt.Errorf("clearance 模式为 none，无需刷新")
	case infraegress.ClearanceModeManual:
		return s.applyManual(ctx, policy)
	case infraegress.ClearanceModeFlareSolverr:
		return s.applyFlareSolverr(ctx, policy)
	default:
		return RefreshResult{}, fmt.Errorf("未知 clearance 模式: %s", policy.Mode)
	}
}

func (s *Service) applyManual(ctx context.Context, policy infraegress.ClearancePolicy) (RefreshResult, error) {
	cookies := egressapp.SanitizeCloudflareCookies(policy.CFCookies)
	if cookies == "" && strings.TrimSpace(policy.UserAgent) == "" {
		return RefreshResult{}, fmt.Errorf("manual 模式需要配置全局 CF Cookie 或 User-Agent")
	}
	nodes, err := s.listRefreshTargets(ctx)
	if err != nil {
		return RefreshResult{}, err
	}
	var result RefreshResult
	for _, node := range nodes {
		if !node.Enabled {
			result.Skipped++
			continue
		}
		if err := s.writeNodeClearance(ctx, node, cookies, policy.UserAgent); err != nil {
			result.Failed++
			s.logger.Warn("clearance_manual_apply_failed", "node", node.Name, "error", err)
			continue
		}
		result.Updated++
	}
	return result, nil
}

func (s *Service) applyFlareSolverr(ctx context.Context, policy infraegress.ClearancePolicy) (RefreshResult, error) {
	if strings.TrimSpace(policy.FlareSolverrURL) == "" {
		return RefreshResult{}, fmt.Errorf("FlareSolverr URL 未配置")
	}
	nodes, err := s.listRefreshTargets(ctx)
	if err != nil {
		return RefreshResult{}, err
	}
	var result RefreshResult
	for _, node := range nodes {
		if !node.Enabled {
			result.Skipped++
			continue
		}
		proxyURL, err := s.cipher.Decrypt(node.EncryptedProxyURL)
		if err != nil {
			result.Failed++
			continue
		}
		proxyURL, err = egressapp.NormalizeProxyURL(proxyURL)
		if err != nil {
			result.Failed++
			continue
		}
		target := "https://grok.com"
		if node.Scope == domainegress.ScopeConsole {
			target = "https://console.x.ai"
		}
		bundle, err := infraegress.RefreshClearanceViaFlareSolverr(ctx, s.client, policy.FlareSolverrURL, proxyURL, target, policy.Timeout)
		if err != nil {
			result.Failed++
			s.logger.Warn("clearance_flaresolverr_failed", "node", node.Name, "error", err)
			continue
		}
		ua := bundle.UserAgent
		if ua == "" {
			ua = policy.UserAgent
		}
		if err := s.writeNodeClearance(ctx, node, bundle.CFCookies, ua); err != nil {
			result.Failed++
			continue
		}
		result.Updated++
	}
	return result, nil
}

func (s *Service) listRefreshTargets(ctx context.Context) ([]domainegress.Node, error) {
	scopes := []domainegress.Scope{domainegress.ScopeWeb, domainegress.ScopeConsole, domainegress.ScopeWebAsset}
	values := make([]domainegress.Node, 0)
	for _, scope := range scopes {
		nodes, err := s.repository.ListEgressNodes(ctx, scope, repository.SortQuery{})
		if err != nil {
			return nil, err
		}
		values = append(values, nodes...)
	}
	return values, nil
}

func (s *Service) writeNodeClearance(ctx context.Context, node domainegress.Node, cookies, userAgent string) error {
	if cookies != "" {
		encrypted, err := s.cipher.Encrypt(cookies)
		if err != nil {
			return err
		}
		node.EncryptedCloudflareCookie = encrypted
	}
	if ua := strings.TrimSpace(userAgent); ua != "" {
		node.UserAgent = ua
	}
	node.LastError = ""
	node.FailureCount = 0
	node.CooldownUntil = nil
	if node.Health < 0.5 {
		node.Health = 0.8
	}
	_, err := s.repository.UpdateEgressNode(ctx, node)
	return err
}

func (s *Service) currentPolicy() infraegress.ClearancePolicy {
	if s.policy == nil {
		return infraegress.DefaultClearancePolicy()
	}
	return s.policy()
}
