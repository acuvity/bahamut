package gateway

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// anyUpstreamer sends everything to one place, so that what a test
// observes is the buffering and nothing else.
type anyUpstreamer struct{ srv *httptest.Server }

func (u *anyUpstreamer) Upstream(req *http.Request) (string, error) {
	return strings.Replace(u.srv.URL, "https://", "", 1), nil
}

// A declared identity reaches its upstream with a body larger than the
// buffer would ever have accepted, and every other identity keeps being
// refused at the same limit. One gateway, both behaviours, which is the
// whole point: the exemption is per identity, not a global loosening.
func TestStreamingIdentitiesBypassTheBufferPerIdentity(t *testing.T) {

	const maxBody = 4096
	const bodySize = 64 * 1024

	var received int64
	ups := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		received = n
		w.WriteHeader(http.StatusOK)
	}))
	defer ups.Close()

	gw, err := New(
		"127.0.0.1:7766",
		&anyUpstreamer{srv: ups},
		OptionUpstreamTLSConfig(&tls.Config{InsecureSkipVerify: true}), // #nosec G402
		OptionBufferRequestLimits(1024, maxBody),
		OptionIdentityStreaming("analyze"),
	)
	if err != nil {
		t.Fatalf("unable to build the gateway: %v", err)
	}
	defer gw.Stop()

	gw.Start()
	defer func() { <-time.After(100 * time.Millisecond) }()

	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // #nosec G402
	}

	post := func(path string) (int, int64) {
		received = 0
		req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:7766"+path, bytes.NewReader(make([]byte, bodySize)))
		resp, rerr := client.Do(req)
		if rerr != nil {
			t.Fatalf("post %s: %v", path, rerr)
		}
		defer resp.Body.Close() // nolint
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, received
	}

	for _, path := range []string{"/analyze", "/v/1/analyze"} {
		t.Run(fmt.Sprintf("streams%s", path), func(t *testing.T) {
			code, got := post(path)
			if code != http.StatusOK {
				t.Fatalf("status=%d, want 200: the body was refused instead of streamed", code)
			}
			if got != bodySize {
				t.Fatalf("upstream received %d bytes, want %d", got, bodySize)
			}
		})
	}

	t.Run("everything_else_is_still_buffered", func(t *testing.T) {
		code, _ := post("/analyzers")
		if code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d, want 413: a non streaming identity must keep its limit", code)
		}
	})
}

// An identity can keep a ceiling and still have its own: bigger than the
// default for what carries documents, unchanged for everything else.
// Unlike streaming this refuses eventually, which is the point of it.
func TestIdentityBufferLimitsApplyPerIdentity(t *testing.T) {

	var received int64
	ups := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		received = n
		w.WriteHeader(http.StatusOK)
	}))
	defer ups.Close()

	gw, err := New(
		"127.0.0.1:7767",
		&anyUpstreamer{srv: ups},
		OptionUpstreamTLSConfig(&tls.Config{InsecureSkipVerify: true}), // #nosec G402
		OptionBufferRequestLimits(1024, 4096),
		OptionIdentityBufferRequestLimits("analyze", 1024, 128*1024),
	)
	if err != nil {
		t.Fatalf("unable to build the gateway: %v", err)
	}
	defer gw.Stop()

	gw.Start()
	defer func() { <-time.After(100 * time.Millisecond) }()

	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // #nosec G402
	}

	post := func(path string, size int) (int, int64) {
		received = 0
		req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:7767"+path, bytes.NewReader(make([]byte, size)))
		resp, rerr := client.Do(req)
		if rerr != nil {
			t.Fatalf("post %s: %v", path, rerr)
		}
		defer resp.Body.Close() // nolint
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, received
	}

	t.Run("under_its_own_limit_it_passes", func(t *testing.T) {
		code, got := post("/analyze", 64*1024)
		if code != http.StatusOK || got != 64*1024 {
			t.Fatalf("status=%d received=%d, want 200 and 65536", code, got)
		}
	})

	t.Run("over_its_own_limit_it_is_still_refused", func(t *testing.T) {
		if code, _ := post("/analyze", 256*1024); code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d, want 413: its own limit must still be a limit", code)
		}
	})

	t.Run("everyone_else_keeps_the_default", func(t *testing.T) {
		if code, _ := post("/analyzers", 64*1024); code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d, want 413: the default must not move", code)
		}
	})
}

// Declaring an identity both streaming and limited is a contradiction, not
// a preference: the limits could never apply. Refuse rather than pick one,
// so the mistake surfaces at boot instead of as a ceiling that silently
// was not there.
func TestAStreamingIdentityGivenBufferLimitsIsRefused(t *testing.T) {

	ups := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ups.Close()

	_, err := New(
		"127.0.0.1:7768",
		&anyUpstreamer{srv: ups},
		OptionUpstreamTLSConfig(&tls.Config{InsecureSkipVerify: true}), // #nosec G402
		OptionIdentityStreaming("analyze"),
		OptionIdentityStreaming("upload"),
		OptionIdentityBufferRequestLimits("analyze", 1024, 4096),
		OptionIdentityBufferRequestLimits("upload", 1024, 4096),
		OptionIdentityBufferRequestLimits("analyzers", 1024, 4096),
	)

	if err == nil {
		t.Fatal("err is nil, want the contradiction refused")
	}
	if !strings.Contains(err.Error(), "analyze, upload") {
		t.Fatalf("err=%v, want both offending identities named and not the innocent one", err)
	}
}

// Setting the global limits alongside a streaming identity is legitimate:
// they hold for everything else. Say which identities they will not reach
// so the number is not read as covering all of them.
func TestGlobalBufferLimitsWarnAboutStreamingIdentities(t *testing.T) {

	logs := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(previous)

	ups := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ups.Close()

	gw, err := New(
		"127.0.0.1:7769",
		&anyUpstreamer{srv: ups},
		OptionUpstreamTLSConfig(&tls.Config{InsecureSkipVerify: true}), // #nosec G402
		OptionBufferRequestLimits(1024, 4096),
		OptionIdentityStreaming("upload"),
		OptionIdentityStreaming("analyze"),
	)
	if err != nil {
		t.Fatalf("unable to build the gateway: %v", err)
	}
	defer gw.Stop()

	out := logs.String()
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("logs=%q, want a warning", out)
	}
	if !strings.Contains(out, "analyze, upload") {
		t.Fatalf("logs=%q, want every streaming identity named, in a stable order", out)
	}
}

// Without the global limits there is nothing to disclaim, so nothing is
// said: a warning that fires on every boot stops being read.
func TestNoWarningWithoutGlobalBufferLimits(t *testing.T) {

	logs := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(previous)

	ups := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ups.Close()

	gw, err := New(
		"127.0.0.1:7770",
		&anyUpstreamer{srv: ups},
		OptionUpstreamTLSConfig(&tls.Config{InsecureSkipVerify: true}), // #nosec G402
		OptionIdentityStreaming("analyze"),
	)
	if err != nil {
		t.Fatalf("unable to build the gateway: %v", err)
	}
	defer gw.Stop()

	if strings.Contains(logs.String(), "do not apply") {
		t.Fatalf("logs=%q, want nothing to disclaim", logs.String())
	}
}

// Zero is not "no limit", it is "whatever the gateway uses": an identity
// with one unset must not end up unbounded on a shared gateway.
func TestZeroIdentityBufferLimitsTakeTheGatewayLimits(t *testing.T) {

	ups := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer ups.Close()

	gw, err := New(
		"127.0.0.1:7771",
		&anyUpstreamer{srv: ups},
		OptionUpstreamTLSConfig(&tls.Config{InsecureSkipVerify: true}), // #nosec G402
		OptionBufferRequestLimits(1024, 4096),
		OptionIdentityBufferRequestLimits("analyze", 0, 0),
	)
	if err != nil {
		t.Fatalf("unable to build the gateway: %v", err)
	}
	defer gw.Stop()

	gw.Start()
	defer func() { <-time.After(100 * time.Millisecond) }()

	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // #nosec G402
	}

	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:7771/analyze", bytes.NewReader(make([]byte, 64*1024)))
	resp, rerr := client.Do(req)
	if rerr != nil {
		t.Fatalf("post: %v", rerr)
	}
	defer resp.Body.Close() // nolint
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want 413: a zero must inherit the gateway max, not remove it", resp.StatusCode)
	}
}
