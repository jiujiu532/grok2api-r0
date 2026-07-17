package web

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	egressdomain "github.com/chenyme/grok2api/backend/internal/domain/egress"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

func TestChatTerminal403FeedbackOccursOncePerAttempt(t *testing.T) {
	// Given
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(writer, "forbidden")
	}))
	defer server.Close()
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	token, err := cipher.Encrypt("test-sso")
	if err != nil {
		t.Fatal(err)
	}
	repo := &feedbackEgressRepository{node: egressdomain.Node{ID: 1, Name: "web", Scope: egressdomain.ScopeWeb, Enabled: true, Health: 1}}
	adapter := NewAdapter(Config{BaseURL: server.URL, ChatTimeoutSeconds: 5, StatsigMode: "manual"}, infraegress.NewManager(repo, cipher), cipher, nil, nil)

	// When
	response, err := adapter.ForwardResponse(context.Background(), provider.ResponseResourceRequest{
		Credential: account.Credential{ID: 1, Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, EncryptedAccessToken: token},
		Method:     http.MethodPost, Path: "/responses", Model: "grok-chat-auto", Operation: "responses", Body: []byte(`{"model":"grok-chat-auto","input":"hello"}`),
	})

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || repo.updateCount() != 2 {
		t.Fatalf("calls=%d feedback=%d", calls.Load(), repo.updateCount())
	}
}

type feedbackEgressRepository struct {
	mu      sync.Mutex
	node    egressdomain.Node
	updates []egressdomain.Node
}

func (r *feedbackEgressRepository) ListEgressNodes(context.Context, egressdomain.Scope, repository.SortQuery) ([]egressdomain.Node, error) {
	return []egressdomain.Node{r.node}, nil
}

func (r *feedbackEgressRepository) GetEgressNode(context.Context, uint64) (egressdomain.Node, error) {
	return r.node, nil
}

func (r *feedbackEgressRepository) CreateEgressNode(context.Context, egressdomain.Node) (egressdomain.Node, error) {
	return egressdomain.Node{}, errors.New("unsupported")
}

func (r *feedbackEgressRepository) UpdateEgressNode(_ context.Context, node egressdomain.Node) (egressdomain.Node, error) {
	r.mu.Lock()
	r.updates = append(r.updates, node)
	r.mu.Unlock()
	return node, nil
}

func (r *feedbackEgressRepository) DeleteEgressNode(context.Context, uint64) error {
	return errors.New("unsupported")
}

func (r *feedbackEgressRepository) updateCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.updates)
}
