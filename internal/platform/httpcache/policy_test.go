package httpcache_test

import (
	"net/http/httptest"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
)

func TestPrivateAndNoStorePolicies(t *testing.T) {
	private := httptest.NewRecorder()
	httpcache.Private(private)
	if got := private.Header().Get("Cache-Control"); got != "private, no-store, no-cache, must-revalidate" {
		t.Fatalf("private policy = %q", got)
	}
	if got := private.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("private pragma = %q", got)
	}

	noStore := httptest.NewRecorder()
	httpcache.NoStore(noStore)
	if got := noStore.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("no-store policy = %q", got)
	}
}

func TestPublicPolicyIsExplicitAndBounded(t *testing.T) {
	recorder := httptest.NewRecorder()
	httpcache.Public(recorder, 60)
	if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Fatalf("public policy = %q", got)
	}
	if got := recorder.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("public vary = %q", got)
	}

	recorder = httptest.NewRecorder()
	httpcache.Public(recorder, -1)
	if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=0" {
		t.Fatalf("negative max-age policy = %q", got)
	}

	recorder = httptest.NewRecorder()
	recorder.Header().Add("Set-Cookie", "arena_session=opaque; HttpOnly; Secure")
	httpcache.Public(recorder, 60)
	if got := recorder.Header().Get("Cache-Control"); got != "private, no-store, no-cache, must-revalidate" {
		t.Fatalf("cookie-bearing response policy = %q", got)
	}
}

func TestValidatorsUseStableFactsAndWeakMatching(t *testing.T) {
	body := []byte(`{"facts":"same"}`)
	if got := httpcache.Validator(body); got != httpcache.Validator(body) {
		t.Fatal("strong validator is not deterministic")
	}
	weak := httpcache.WeakValidator(body)
	if weak[:2] != "W/" {
		t.Fatalf("weak validator = %q", weak)
	}
	for _, header := range []string{weak, weak[2:], "W/" + weak[2:], "*"} {
		if !httpcache.Matches(header, weak) {
			t.Fatalf("Matches(%q, %q) = false", header, weak)
		}
	}
	if httpcache.Matches(`"different"`, weak) {
		t.Fatal("different validator matched")
	}
}
