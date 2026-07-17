package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	application "github.com/chenyme/grok2api/backend/internal/application/egress"
)

const (
	defaultClearanceTarget = "https://grok.com"
	flareResponseLimit     = 2 << 20
)

// ClearanceBundle 是一次 clearance 刷新的结果。
type ClearanceBundle struct {
	CFCookies string
	UserAgent string
}

type flareSolverrRequest struct {
	Cmd        string            `json:"cmd"`
	URL        string            `json:"url"`
	MaxTimeout int               `json:"maxTimeout"`
	Proxy      *flareSolverProxy `json:"proxy,omitempty"`
}

type flareSolverProxy struct {
	URL string `json:"url"`
}

type flareSolverrResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Solution *struct {
		UserAgent string `json:"userAgent"`
		Cookies   []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"cookies"`
	} `json:"solution"`
}

// RefreshClearanceViaFlareSolverr 通过 FlareSolverr 获取 cf_clearance 等 Cookie。
func RefreshClearanceViaFlareSolverr(ctx context.Context, client *http.Client, endpoint, proxyURL, targetURL string, timeout time.Duration) (ClearanceBundle, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ClearanceBundle{}, fmt.Errorf("FlareSolverr 地址为空")
	}
	if _, err := url.ParseRequestURI(endpoint); err != nil {
		return ClearanceBundle{}, fmt.Errorf("FlareSolverr 地址无效: %w", err)
	}
	if timeout <= 0 {
		timeout = DefaultClearanceTimeout
	}
	target := strings.TrimSpace(targetURL)
	if target == "" {
		target = defaultClearanceTarget
	}
	payload := flareSolverrRequest{
		Cmd:        "request.get",
		URL:        target,
		MaxTimeout: int(timeout / time.Millisecond),
	}
	if proxy := strings.TrimSpace(proxyURL); proxy != "" {
		payload.Proxy = &flareSolverProxy{URL: proxy}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ClearanceBundle{}, err
	}
	requestURL := strings.TrimRight(endpoint, "/") + "/v1"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return ClearanceBundle{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if client == nil {
		client = &http.Client{Timeout: timeout + 15*time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return ClearanceBundle{}, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, flareResponseLimit+1))
	if err != nil {
		return ClearanceBundle{}, err
	}
	if len(raw) > flareResponseLimit {
		return ClearanceBundle{}, fmt.Errorf("FlareSolverr 响应过大")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ClearanceBundle{}, fmt.Errorf("FlareSolverr 返回 HTTP %d", response.StatusCode)
	}
	var parsed flareSolverrResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ClearanceBundle{}, fmt.Errorf("解析 FlareSolverr 响应: %w", err)
	}
	if !strings.EqualFold(parsed.Status, "ok") || parsed.Solution == nil {
		message := strings.TrimSpace(parsed.Message)
		if message == "" {
			message = "unknown error"
		}
		return ClearanceBundle{}, fmt.Errorf("FlareSolverr 失败: %s", message)
	}
	parts := make([]string, 0, len(parsed.Solution.Cookies))
	for _, cookie := range parsed.Solution.Cookies {
		name := strings.TrimSpace(cookie.Name)
		if name == "" {
			continue
		}
		parts = append(parts, name+"="+strings.TrimSpace(cookie.Value))
	}
	cookies := application.SanitizeCloudflareCookies(strings.Join(parts, "; "))
	if cookies == "" {
		return ClearanceBundle{}, fmt.Errorf("FlareSolverr 未返回有效 Cloudflare Cookie")
	}
	return ClearanceBundle{
		CFCookies: cookies,
		UserAgent: strings.TrimSpace(parsed.Solution.UserAgent),
	}, nil
}
