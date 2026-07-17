package egress

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshClearanceViaFlareSolverr_rejectsRedirects(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests.Add(1)
	}))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	t.Cleanup(redirect.Close)

	// When
	_, err := RefreshClearanceViaFlareSolverr(context.Background(), nil, redirect.URL, "", "", time.Second)

	// Then
	if err == nil {
		t.Fatal("redirected FlareSolverr response unexpectedly succeeded")
	}
	if targetRequests.Load() != 0 {
		t.Fatal("FlareSolverr client followed a redirect")
	}
}

func TestRefreshClearanceViaFlareSolverr_doesNotExposeProxyCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"error","message":"proxy socks5://user:password@example.com:1080 rejected"}`))
	}))
	t.Cleanup(server.Close)

	// When
	_, err := RefreshClearanceViaFlareSolverr(context.Background(), nil, server.URL, "socks5://user:password@example.com:1080", "", time.Second)

	// Then
	if err == nil {
		t.Fatal("failed FlareSolverr response unexpectedly succeeded")
	}
	if got := err.Error(); got == "" || containsProxyCredential(got) {
		t.Fatalf("FlareSolverr error exposed proxy credentials: %q", got)
	}
}

func containsProxyCredential(value string) bool {
	return value == "proxy socks5://user:password@example.com:1080 rejected" ||
		(len(value) >= len("user:password") && contains(value, "user:password"))
}

func contains(value, substring string) bool {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return true
		}
	}
	return false
}
