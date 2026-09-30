package profiling_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/platform/profiling"
)

func TestHandlerExposesProfilesOnThePrivateSurface(t *testing.T) {
	handler := profiling.Handler()
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/goroutine", "/debug/pprof/heap"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, recorder.Code)
		}
	}
}

func TestHandlerDoesNotAcceptUnexpectedMethods(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/debug/pprof/goroutine", nil)
	profiling.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST goroutine profile status = %d, want 405", recorder.Code)
	}
}

func BenchmarkPrivateProfileIndex(b *testing.B) {
	handler := profiling.Handler()
	request := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	for i := 0; i < b.N; i++ {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			b.Fatalf("profile index status = %d", recorder.Code)
		}
	}
}
