package web

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	domainegress "github.com/chenyme/grok2api/backend/internal/domain/egress"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
)

func TestPostJSONWithRefererRetriesOnceAfterForbiddenWithManualStatsig(t *testing.T) {
	// Given
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, `{}`)
	}))
	defer server.Close()
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	manager := infraegress.NewManager(egressRepositoryStub{}, cipher)
	lease, err := manager.Acquire(context.Background(), domainegress.ScopeWeb, "image")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	adapter := &Adapter{cfg: Config{BaseURL: server.URL, StatsigMode: "manual"}, egress: manager}

	// When
	response, err := adapter.postJSONWithReferer(context.Background(), adapter.cfg, lease, "test-sso", server.URL+"/rest/media/post/create", map[string]string{"prompt": "draw"}, time.Second, server.URL+"/imagine")

	// Then
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("status=%d calls=%d", response.StatusCode, calls.Load())
	}
}
