// Tests of internal/platform/clientip (P16-T03): who the client is, and the
// spoof that must not work.
//
// The property under test is not "the header is parsed" but "the header is
// evidence only when the peer earned it". Every case below therefore names the
// peer, the header and the address that must be returned.
package clientip_test

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
)

// request builds a request with a peer address and forwarding headers.
func request(remoteAddr string, forwarded ...string) *http.Request {
	httpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	httpRequest.RemoteAddr = remoteAddr
	if len(forwarded) > 0 {
		httpRequest.Header.Set("X-Forwarded-For", forwarded[0])
	}
	if len(forwarded) > 1 {
		httpRequest.Header.Set("X-Real-IP", forwarded[1])
		httpRequest.Header.Set("CF-Connecting-IP", forwarded[1])
	}
	return httpRequest
}

func mustPrefixes(t *testing.T, values ...string) []netip.Prefix {
	t.Helper()

	prefixes, err := clientip.ParseTrusted(values)
	if err != nil {
		t.Fatalf("ParseTrusted(%v) error = %v", values, err)
	}
	return prefixes
}

func TestClientIgnoresForwardingHeadersFromUntrustedPeers(t *testing.T) {
	t.Parallel()

	// The shipped default: nobody is trusted. This is the case that matters
	// most, because every deployment gets it unless it says otherwise.
	resolver := clientip.New(nil)
	if resolver.Count() != 0 {
		t.Fatalf("New(nil).Count() = %d, want 0", resolver.Count())
	}

	scenarios := []struct {
		name       string
		remoteAddr string
		forwarded  string
		want       string
	}{
		{"no header", "198.51.100.7:44321", "", "198.51.100.7"},
		{"spoofed ipv4", "198.51.100.7:44321", "6.6.6.6", "198.51.100.7"},
		{"spoofed chain", "198.51.100.7:44321", "6.6.6.6, 7.7.7.7", "198.51.100.7"},
		{"spoofed loopback", "198.51.100.7:44321", "127.0.0.1", "198.51.100.7"},
		{"ipv6 peer", "[2001:db8::7]:51000", "6.6.6.6", "2001:db8::7"},
		{"ipv4 mapped peer is ipv4", "[::ffff:198.51.100.7]:51000", "", "198.51.100.7"},
		{"port on the forwarded entry", "198.51.100.7:44321", "6.6.6.6:1234", "198.51.100.7"},
		{"garbage header", "198.51.100.7:44321", "not-an-address", "198.51.100.7"},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()

			got := resolver.Client(request(scenario.remoteAddr, scenario.forwarded))
			want, err := netip.ParseAddr(scenario.want)
			if err != nil {
				t.Fatalf("ParseAddr(%q) error = %v", scenario.want, err)
			}
			if got != want.Unmap() {
				t.Errorf("Client() = %s, want %s (peer %s, header %q)", got, want, scenario.remoteAddr, scenario.forwarded)
			}
		})
	}
}

func TestClientReadsTheChainWhenThePeerIsATrustedProxy(t *testing.T) {
	t.Parallel()

	// The edge configuration of docs/SECURITY.md: Cloudflare and Caddy in
	// front, the application behind them. Trust is a prefix set, never a
	// single header.
	resolver := clientip.New(mustPrefixes(t, "10.0.0.0/8", "172.16.0.0/12", "127.0.0.1/32"))

	scenarios := []struct {
		name       string
		remoteAddr string
		forwarded  string
		want       string
	}{
		{"last hop appended the client", "10.0.0.5:1234", "198.51.100.7", "198.51.100.7"},
		{"proxy chain, client is the first entry", "10.0.0.5:1234", "198.51.100.7, 172.16.0.9", "198.51.100.7"},
		{"client inside the trusted range spoofs past itself", "10.0.0.5:1234", "6.6.6.6, 10.0.0.7", "6.6.6.6"},
		{"spoofed prefix before the real address", "10.0.0.5:1234", "6.6.6.6, 198.51.100.7", "198.51.100.7"},
		{"every entry trusted", "10.0.0.5:1234", "10.1.2.3, 10.0.0.7", "10.1.2.3"},
		{"header absent", "10.0.0.5:1234", "", "10.0.0.5"},
		{"ipv6 client through the proxy", "10.0.0.5:1234", "2001:db8::9", "2001:db8::9"},
		{"mapped ipv6 client is the ipv4 client", "10.0.0.5:1234", "::ffff:198.51.100.7", "198.51.100.7"},
		{"loopback peer is trusted by default in development", "127.0.0.1:1234", "198.51.100.7", "198.51.100.7"},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()

			got := resolver.Client(request(scenario.remoteAddr, scenario.forwarded))
			want, err := netip.ParseAddr(scenario.want)
			if err != nil {
				t.Fatalf("ParseAddr(%q) error = %v", scenario.want, err)
			}
			if got != want.Unmap() {
				t.Errorf("Client() = %s, want %s (peer %s, header %q)", got, want, scenario.remoteAddr, scenario.forwarded)
			}
		})
	}
}

func TestSingleValuedHeadersAreNeverTrusted(t *testing.T) {
	t.Parallel()

	// X-Real-IP and CF-Connecting-IP have no chain, so they cannot be
	// verified by the application: behind a proxy that forwards client
	// headers, the client could have written them. A trusted peer therefore
	// does not make them evidence.
	resolver := clientip.New(mustPrefixes(t, "10.0.0.0/8"))

	httpRequest := request("10.0.0.5:1234")
	httpRequest.Header.Set("X-Real-IP", "6.6.6.6")
	httpRequest.Header.Set("CF-Connecting-IP", "7.7.7.7")
	httpRequest.Header.Set("Forwarded", "for=8.8.8.8")

	got := resolver.Client(httpRequest)
	want := netip.MustParseAddr("10.0.0.5")
	if got != want {
		t.Errorf("Client() = %s, want the peer %s: no single-valued header may key an account", got, want)
	}
}

func TestUnknownPeerIsReportedAsInvalidRatherThanGuessed(t *testing.T) {
	t.Parallel()

	resolver := clientip.New(nil)

	for _, remoteAddr := range []string{"", "garbage", "1.2.3.4.5:99"} {
		got := resolver.Client(request(remoteAddr, "6.6.6.6"))
		if got.IsValid() {
			t.Errorf("Client(RemoteAddr=%q) = %s, want the zero address: an unknown peer must not be guessed", remoteAddr, got)
		}
	}
}

func TestTrustedHeaderEntriesAreBounded(t *testing.T) {
	t.Parallel()

	resolver := clientip.New(mustPrefixes(t, "10.0.0.0/8"))

	// A header long enough to be a denial-of-service attempt on the parser:
	// the walk stays bounded, and the answer is still the entry that the walk
	// would produce, which is the attacker's own address.
	header := ""
	for index := 0; index < 512; index++ {
		header += "10.0.0.1, "
	}
	header += "198.51.100.7"

	got := resolver.Client(request("10.0.0.5:1234", header))
	if !got.IsValid() {
		t.Fatal("Client() returned no address for a bounded header")
	}
	if resolver.Trusts(got) {
		t.Errorf("Client() = %s, want an address outside the trusted range: the walk must not return a proxy as the client", got)
	}
}

func TestParseTrustedRejectsOperatorTypos(t *testing.T) {
	t.Parallel()

	if _, err := clientip.ParseTrusted([]string{"10.0.0.0/8", "not-a-network"}); err == nil {
		t.Error("ParseTrusted() accepted an invalid trusted proxy")
	}

	prefixes := mustPrefixes(t, "10.0.0.7/8", "::ffff:192.168.1.0/120", "2001:db8::/32", "  ")
	if len(prefixes) != 3 {
		t.Fatalf("ParseTrusted() returned %d prefixes, want 3", len(prefixes))
	}

	resolver := clientip.New(prefixes)
	for _, address := range []string{"10.9.9.9", "192.168.1.5", "2001:db8::1"} {
		if !resolver.Trusts(netip.MustParseAddr(address)) {
			t.Errorf("Trusts(%s) = false, want true", address)
		}
	}
	for _, address := range []string{"11.0.0.1", "192.168.2.5", "2001:db9::1"} {
		if resolver.Trusts(netip.MustParseAddr(address)) {
			t.Errorf("Trusts(%s) = true, want false", address)
		}
	}
}

func TestResolverDoesNotMutateItsInput(t *testing.T) {
	t.Parallel()

	trusted := mustPrefixes(t, "10.0.0.0/8")
	resolver := clientip.New(trusted)

	trusted[0] = netip.MustParsePrefix("203.0.113.0/24")

	if resolver.Trusts(netip.MustParseAddr("203.0.113.9")) {
		t.Error("the resolver adopted a prefix added to the caller's slice after construction")
	}
	if !resolver.Trusts(netip.MustParseAddr("10.0.0.9")) {
		t.Error("the resolver lost a prefix the caller replaced")
	}
}
