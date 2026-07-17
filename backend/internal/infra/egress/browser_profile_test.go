package egress

import (
	"net/http"
	"strings"
	"testing"
)

func TestClientProfileFromUserAgentAlignsChromeMajor(t *testing.T) {
	ua := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"
	got := ClientProfileFromUserAgent(ua)
	if !strings.Contains(strings.ToLower(got.GetClientHelloStr()), "chrome") {
		t.Fatalf("expected chrome profile, got %q", got.GetClientHelloStr())
	}
	empty := ClientProfileFromUserAgent("")
	if empty.GetClientHelloStr() == "" {
		t.Fatal("empty UA profile missing client hello")
	}
}

func TestNearestChromeProfileForUnknownHigherVersion(t *testing.T) {
	profile := nearestChromeProfile(200)
	if profile.GetClientHelloStr() == "" {
		t.Fatal("nearest profile empty")
	}
}

func TestApplyClientHintsForChromium(t *testing.T) {
	header := http.Header{}
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
	ApplyClientHints(header, ua)
	if !strings.Contains(header.Get("Sec-Ch-Ua"), `Google Chrome";v="146"`) {
		t.Fatalf("Sec-Ch-Ua = %q", header.Get("Sec-Ch-Ua"))
	}
	if header.Get("Sec-Ch-Ua-Platform") != `"Windows"` {
		t.Fatalf("platform = %q", header.Get("Sec-Ch-Ua-Platform"))
	}
	if header.Get("Sec-Ch-Ua-Mobile") != "?0" {
		t.Fatalf("mobile = %q", header.Get("Sec-Ch-Ua-Mobile"))
	}
}

func TestApplyClientHintsSkipsFirefox(t *testing.T) {
	header := http.Header{}
	ApplyClientHints(header, "Mozilla/5.0 (X11; Linux x86_64; rv:148.0) Gecko/20100101 Firefox/148.0")
	if header.Get("Sec-Ch-Ua") != "" {
		t.Fatalf("unexpected client hints for firefox: %v", header)
	}
}

func TestDefaultClearancePolicy(t *testing.T) {
	policy := DefaultClearancePolicy()
	if !policy.ClientHintsEnabled {
		t.Fatal("client hints should default enabled")
	}
	if policy.AntiBotCooldown <= 0 {
		t.Fatal("anti-bot cooldown should default positive")
	}
}
