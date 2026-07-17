package clearance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	domainegress "github.com/chenyme/grok2api/backend/internal/domain/egress"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

func TestServiceRefreshAll_sharesWorkAndPreservesRefreshAfterWaiterCancellation(t *testing.T) {
	updatesStarted := make(chan struct{}, 2)
	releaseUpdate := make(chan struct{})
	repository := &clearanceRepositoryStub{
		nodes: []domainegress.Node{{ID: 1, Name: "web", Scope: domainegress.ScopeWeb, Enabled: true}},
		update: func(context.Context, domainegress.Node) (domainegress.Node, error) {
			updatesStarted <- struct{}{}
			<-releaseUpdate
			return domainegress.Node{}, nil
		},
	}
	service := NewService(repository, nil, func() infraegress.ClearancePolicy {
		return infraegress.ClearancePolicy{Mode: infraegress.ClearanceModeManual, UserAgent: "agent"}
	}, nil)
	invalidations := atomic.Int32{}
	service.SetCacheInvalidator(func(domainegress.Scope, uint64) { invalidations.Add(1) })
	firstContext, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, err := service.RefreshAll(firstContext)
		firstResult <- err
	}()
	<-updatesStarted
	secondResult := make(chan RefreshResult, 1)
	secondErrors := make(chan error, 1)
	go func() {
		result, err := service.RefreshAll(context.Background())
		secondResult <- result
		secondErrors <- err
	}()
	cancelFirst()

	// Then
	select {
	case err := <-firstResult:
		if err != context.Canceled {
			t.Fatalf("first waiter error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter remained blocked on shared refresh")
	}
	if calls := repository.updateCalls.Load(); calls != 1 {
		t.Fatalf("concurrent refreshes started %d updates, want one", calls)
	}
	close(releaseUpdate)
	if err := <-secondErrors; err != nil {
		t.Fatalf("shared refresh error = %v", err)
	}
	if result := <-secondResult; result.Updated != 1 {
		t.Fatalf("shared refresh result = %#v", result)
	}
	if invalidations.Load() < 1 {
		t.Fatalf("cache invalidations = %d, want at least one", invalidations.Load())
	}
}

func TestServiceRefreshAll_limitsFlareSolverrConcurrencyAndUsesNodeTimeout(t *testing.T) {
	const nodeCount = 5
	started := make(chan struct{}, nodeCount)
	release := make(chan struct{})
	server := newFlareSolverrServer(t, started, release)
	cipher := testCipher(t)
	nodes := make([]domainegress.Node, 0, nodeCount)
	for nodeID := 1; nodeID <= nodeCount; nodeID++ {
		proxy, err := cipher.Encrypt("socks5h://warp:1080")
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, domainegress.Node{ID: uint64(nodeID), Name: "node", Scope: domainegress.ScopeWeb, Enabled: true, EncryptedProxyURL: proxy})
	}
	repository := &clearanceRepositoryStub{nodes: nodes}
	service := NewService(repository, cipher, func() infraegress.ClearancePolicy {
		return infraegress.ClearancePolicy{Mode: infraegress.ClearanceModeFlareSolverr, FlareSolverrURL: server.URL, Timeout: 75 * time.Millisecond}
	}, nil)
	service.SetRefreshConcurrency(2)

	result := make(chan RefreshResult, 1)
	errs := make(chan error, 1)
	go func() {
		value, err := service.RefreshAll(context.Background())
		result <- value
		errs <- err
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("FlareSolverr refresh did not reach the configured concurrency limit of 2")
		}
	}
	select {
	case <-started:
		t.Fatal("FlareSolverr refresh exceeded the concurrency limit")
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-errs; err != nil {
		t.Fatalf("RefreshAll error = %v", err)
	}
	if value := <-result; value.Updated != nodeCount {
		t.Fatalf("RefreshAll result = %#v", value)
	}
}

func TestServiceRefreshAll_appliesConfiguredTimeoutToEachFlareSolverrNode(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	t.Cleanup(server.Close)
	cipher := testCipher(t)
	proxy, err := cipher.Encrypt("socks5h://warp:1080")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(&clearanceRepositoryStub{nodes: []domainegress.Node{{
		ID: 1, Name: "web", Scope: domainegress.ScopeWeb, Enabled: true, EncryptedProxyURL: proxy,
	}}}, cipher, func() infraegress.ClearancePolicy {
		return infraegress.ClearancePolicy{Mode: infraegress.ClearanceModeFlareSolverr, FlareSolverrURL: server.URL, Timeout: 50 * time.Millisecond}
	}, nil)

	// When
	result, refreshErr := service.RefreshAll(context.Background())

	// Then
	if refreshErr != nil || result.Failed != 1 {
		t.Fatalf("RefreshAll result = %#v, error = %v", result, refreshErr)
	}
	close(release)
}

type clearanceRepositoryStub struct {
	nodes       []domainegress.Node
	update      func(context.Context, domainegress.Node) (domainegress.Node, error)
	updateCalls atomic.Int32
}

func (r *clearanceRepositoryStub) ListEgressNodes(_ context.Context, scope domainegress.Scope, _ repository.SortQuery) ([]domainegress.Node, error) {
	values := make([]domainegress.Node, 0, len(r.nodes))
	for _, node := range r.nodes {
		if node.Scope == scope {
			values = append(values, node)
		}
	}
	return values, nil
}

func (r *clearanceRepositoryStub) GetEgressNode(context.Context, uint64) (domainegress.Node, error) {
	return domainegress.Node{}, nil
}

func (r *clearanceRepositoryStub) CreateEgressNode(context.Context, domainegress.Node) (domainegress.Node, error) {
	return domainegress.Node{}, nil
}

func (r *clearanceRepositoryStub) UpdateEgressNode(ctx context.Context, node domainegress.Node) (domainegress.Node, error) {
	r.updateCalls.Add(1)
	if r.update != nil {
		return r.update(ctx, node)
	}
	return node, nil
}

func (r *clearanceRepositoryStub) DeleteEgressNode(context.Context, uint64) error { return nil }

func newFlareSolverrServer(t *testing.T, started chan<- struct{}, release <-chan struct{}) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ok","solution":{"userAgent":"agent","cookies":[{"name":"cf_clearance","value":"value"}]}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func testCipher(t *testing.T) *security.Cipher {
	t.Helper()
	cipher, err := security.NewCipher("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}
