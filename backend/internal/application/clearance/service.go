package clearance

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	egressapp "github.com/chenyme/grok2api/backend/internal/application/egress"
	domainegress "github.com/chenyme/grok2api/backend/internal/domain/egress"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

// PolicySource 提供当前 clearance 策略。
type PolicySource func() infraegress.ClearancePolicy

const defaultRefreshConcurrency = 4

// Service 负责 FlareSolverr / 手动 clearance 刷新。
type Service struct {
	mu                 sync.Mutex
	repository         repository.EgressRepository
	cipher             *security.Cipher
	policy             PolicySource
	client             *http.Client
	logger             *slog.Logger
	cacheInvalidator   func(domainegress.Scope, uint64)
	lastRun            time.Time
	refresh            *refreshExecution
	refreshConcurrency int
}

type refreshExecution struct {
	done   chan struct{}
	result RefreshResult
	err    error
}

func NewService(repository repository.EgressRepository, cipher *security.Cipher, policy PolicySource, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		repository: repository,
		cipher:     cipher,
		policy:     policy,
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		logger:             logger,
		refreshConcurrency: defaultRefreshConcurrency,
	}
}

// SetCacheInvalidator 配置节点 clearance 写入后的 scoped egress 缓存失效回调。
func (s *Service) SetCacheInvalidator(invalidator func(domainegress.Scope, uint64)) {
	s.mu.Lock()
	s.cacheInvalidator = invalidator
	s.mu.Unlock()
}

// SetRefreshConcurrency 更新 FlareSolverr 节点刷新的并发上限。
func (s *Service) SetRefreshConcurrency(limit int) {
	if limit <= 0 {
		limit = defaultRefreshConcurrency
	}
	s.mu.Lock()
	s.refreshConcurrency = limit
	s.mu.Unlock()
}

func (s *Service) currentRefreshConcurrency() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshConcurrency
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
	s.mu.Lock()
	execution := s.refresh
	if execution == nil {
		execution = &refreshExecution{done: make(chan struct{})}
		s.refresh = execution
		go s.runRefresh(execution)
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return RefreshResult{}, ctx.Err()
	case <-execution.done:
		return execution.result, execution.err
	}
}

func (s *Service) runRefresh(execution *refreshExecution) {
	policy := s.currentPolicy()
	result, err := s.refreshAll(context.Background(), policy)
	s.mu.Lock()
	execution.result = result
	execution.err = err
	s.refresh = nil
	close(execution.done)
	s.mu.Unlock()
}

func (s *Service) refreshAll(ctx context.Context, policy infraegress.ClearancePolicy) (RefreshResult, error) {
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

func (s *Service) currentPolicy() infraegress.ClearancePolicy {
	if s.policy == nil {
		return infraegress.DefaultClearancePolicy()
	}
	return s.policy()
}
