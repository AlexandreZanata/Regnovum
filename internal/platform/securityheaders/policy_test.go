package securityheaders_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// TestPolicyIsWhatTheMiddlewareWrites is the premise of every gate that
// compares another component's copy of the policy against this package: the
// exported set and the delivered headers are the same fields, with the same
// values, in the same order. A difference between them would make the
// comparison measure the description instead of the behaviour.
func TestPolicyIsWhatTheMiddlewareWrites(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name       string
		production bool
	}{
		{name: "development", production: false},
		{name: "production", production: true},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			policy := securityheaders.Policy(testCase.production)
			if len(policy) == 0 {
				t.Fatal("Policy() returned nothing; a middleware built from it would deliver no policy")
			}

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			securityheaders.Middleware(securityheaders.Config{Production: testCase.production})(
				http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
			).ServeHTTP(recorder, request)

			delivered := recorder.Result().Header
			if len(delivered) != len(policy) {
				t.Fatalf("the middleware delivered %d field(s) and Policy() describes %d: %v vs %v",
					len(delivered), len(policy), delivered, policy)
			}
			for _, field := range policy {
				if got := delivered.Get(field.Name); got != field.Value {
					t.Errorf("middleware wrote %s = %q, Policy() describes %q", field.Name, got, field.Value)
				}
			}
		})
	}
}

// TestTransportSecurityIsTheOnlyFieldProductionAdds pins the one difference
// between the two environments. HSTS is not cosmetic — a browser that stores
// it while developing against plain HTTP makes the development server
// unreachable until it expires — so the difference is asserted rather than
// assumed, and a third field arriving in production would have to be declared
// in this test.
func TestTransportSecurityIsTheOnlyFieldProductionAdds(t *testing.T) {
	t.Parallel()

	const hsts = "Strict-Transport-Security"

	development := securityheaders.Policy(false)
	production := securityheaders.Policy(true)

	if header(development, hsts) != "" {
		t.Errorf("the development policy carries %s, which is only safe once the deployment serves HTTPS end to end", hsts)
	}
	if header(production, hsts) == "" {
		t.Errorf("the production policy does not carry %s: a visitor's first https request would be the only one that is protected", hsts)
	}
	for _, field := range development {
		if got := header(production, field.Name); got != field.Value {
			t.Errorf("production changed %s to %q, and the two environments differ only by %s", field.Name, got, hsts)
		}
	}
	if len(production) != len(development)+1 {
		t.Errorf("production describes %d field(s) and development %d, want exactly one more", len(production), len(development))
	}
}

// header returns the value of one field of a policy, or the empty string when
// the field is not in it.
func header(policy []securityheaders.Header, name string) string {
	for _, field := range policy {
		if field.Name == name {
			return field.Value
		}
	}
	return ""
}
