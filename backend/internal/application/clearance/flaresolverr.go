package clearance

import (
	"context"
	"fmt"
	"strings"
	"sync"

	egressapp "github.com/chenyme/grok2api/backend/internal/application/egress"
	domainegress "github.com/chenyme/grok2api/backend/internal/domain/egress"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
)

func (s *Service) applyFlareSolverr(ctx context.Context, policy infraegress.ClearancePolicy) (RefreshResult, error) {
	if strings.TrimSpace(policy.FlareSolverrURL) == "" {
		return RefreshResult{}, fmt.Errorf("FlareSolverr URL 未配置")
	}
	nodes, err := s.listRefreshTargets(ctx)
	if err != nil {
		return RefreshResult{}, err
	}
	var (
		result   RefreshResult
		resultMu sync.Mutex
		workers  = make(chan struct{}, s.currentRefreshConcurrency())
		group    sync.WaitGroup
	)
	for _, node := range nodes {
		if !node.Enabled {
			resultMu.Lock()
			result.Skipped++
			resultMu.Unlock()
			continue
		}
		group.Add(1)
		go func() {
			defer group.Done()
			workers <- struct{}{}
			defer func() { <-workers }()
			proxyURL, err := s.cipher.Decrypt(node.EncryptedProxyURL)
			if err != nil {
				resultMu.Lock()
				result.Failed++
				resultMu.Unlock()
				return
			}
			proxyURL, err = egressapp.NormalizeProxyURL(proxyURL)
			if err != nil {
				resultMu.Lock()
				result.Failed++
				resultMu.Unlock()
				return
			}
			target := "https://grok.com"
			if node.Scope == domainegress.ScopeConsole {
				target = "https://console.x.ai"
			}
			bundle, err := infraegress.RefreshClearanceViaFlareSolverr(ctx, s.client, policy.FlareSolverrURL, proxyURL, target, policy.Timeout)
			if err != nil {
				resultMu.Lock()
				result.Failed++
				resultMu.Unlock()
				s.logger.Warn("clearance_flaresolverr_failed", "node", node.Name, "error", err)
				return
			}
			userAgent := bundle.UserAgent
			if userAgent == "" {
				userAgent = policy.UserAgent
			}
			if err := s.writeNodeClearance(ctx, node, bundle.CFCookies, userAgent); err != nil {
				resultMu.Lock()
				result.Failed++
				resultMu.Unlock()
				return
			}
			resultMu.Lock()
			result.Updated++
			resultMu.Unlock()
		}()
	}
	group.Wait()
	return result, nil
}

func (s *Service) writeNodeClearance(ctx context.Context, node domainegress.Node, cookies, userAgent string) error {
	if cookies != "" {
		encrypted, err := s.cipher.Encrypt(cookies)
		if err != nil {
			return err
		}
		node.EncryptedCloudflareCookie = encrypted
	}
	if userAgent = strings.TrimSpace(userAgent); userAgent != "" {
		node.UserAgent = userAgent
	}
	node.LastError = ""
	node.FailureCount = 0
	node.CooldownUntil = nil
	if node.Health < 0.5 {
		node.Health = 0.8
	}
	if _, err := s.repository.UpdateEgressNode(ctx, node); err != nil {
		return err
	}
	s.mu.Lock()
	invalidator := s.cacheInvalidator
	s.mu.Unlock()
	if invalidator != nil {
		invalidator(node.Scope, node.ID)
	}
	return nil
}
