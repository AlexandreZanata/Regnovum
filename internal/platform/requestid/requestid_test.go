package requestid

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// seqGenerator is a deterministic IDGenerator for tests: it hands out
// numbered identifiers without touching entropy.
type seqGenerator struct {
	next int
}

func (stub *seqGenerator) NewID() string {
	stub.next++
	return "generated-" + strings.Repeat("a", 8) + string(rune('0'+stub.next))
}

var _ ports.IDGenerator = (*seqGenerator)(nil)

func TestValidateAcceptsSafeIdentifiers(t *testing.T) {
	valid := []string{
		"abc-123",
		"Request_47",
		strings.Repeat("a", 64), // exactly at the limit
		"01H8XQ2E",
	}
	for _, value := range valid {
		if !Validate(value) {
			t.Errorf("Validate(%q) = false, want true", value)
		}
	}
}

func TestValidateRejectsUnsafeIdentifiers(t *testing.T) {
	invalid := []string{
		"",
		strings.Repeat("a", 65), // over the limit
		"with space",
		"id;drop",
		"newline\ninjected",
		"tab\tid",
		"ünïcode",
		"emoji🎉id",
	}
	for _, value := range invalid {
		if Validate(value) {
			t.Errorf("Validate(%q) = true, want false", value)
		}
	}
}

// TestMiddlewareEchoesAndCorrelates proves the validation minimum: the
// resolved request ID appears in the response header AND is available in
// the handler context for logging.
func TestMiddlewareEchoesAndCorrelates(t *testing.T) {
	generator := &seqGenerator{}
	var seen string

	handler := Middleware(generator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = FromRequest(request)
		writer.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(Header, "client-provided-id")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if got := recorder.Header().Get(Header); got != "client-provided-id" {
		t.Errorf("response header = %q, want the validated client id", got)
	}
	if seen != "client-provided-id" {
		t.Errorf("handler context id = %q, want the client id", seen)
	}
}

func TestMiddlewareGeneratesWhenMissingOrInvalid(t *testing.T) {
	generator := &seqGenerator{}

	t.Run("absent header", func(t *testing.T) {
		var seen string
		handler := Middleware(generator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			seen = FromRequest(request)
		}))

		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

		if !strings.HasPrefix(seen, "generated-") {
			t.Errorf("context id = %q, want a generated id", seen)
		}
		if got := recorder.Header().Get(Header); got != seen {
			t.Errorf("response header = %q, want the generated id %q", got, seen)
		}
	})

	t.Run("invalid header", func(t *testing.T) {
		var seen string
		handler := Middleware(generator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			seen = FromRequest(request)
		}))

		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set(Header, "bad\nheader")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		if !strings.HasPrefix(seen, "generated-") {
			t.Errorf("context id = %q, want a generated id for the invalid inbound header", seen)
		}
	})
}

func TestFromContextOutsideMiddleware(t *testing.T) {
	//lint:ignore SA1012 o contexto nulo é o caso sob teste: a função tem de responder sem entrar em pânico
	if got := FromContext(nil); got != "" {
		t.Errorf("FromContext(nil) = %q, want empty", got)
	}
	if got := FromContext(t.Context()); got != "" {
		t.Errorf("FromContext(background) = %q, want empty", got)
	}
}

func TestMiddlewareWrapsInnerContext(t *testing.T) {
	generator := &seqGenerator{}

	// Two nested middleware applications keep correlation stable: the inner
	// handler sees a valid id either way.
	inner := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if FromRequest(request) == "" {
			t.Error("inner handler lost the request id")
		}
	})
	outer := Middleware(generator, Middleware(generator, inner))

	recorder := httptest.NewRecorder()
	outer.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
}
