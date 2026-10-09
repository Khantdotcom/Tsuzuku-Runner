package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireWorkerToken(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := requireWorkerToken("secret")(ok)

	tests := []struct {
		name   string
		header string
		want   int
	}{
		{"valid", "Bearer secret", http.StatusNoContent},
		{"scheme is case insensitive", "bearer secret", http.StatusNoContent},
		{"missing header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"token prefix", "Bearer secre", http.StatusUnauthorized},
		{"wrong scheme", "Basic secret", http.StatusUnauthorized},
		{"empty token", "Bearer ", http.StatusUnauthorized},
		{"no scheme", "secret", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/x", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.want == http.StatusUnauthorized {
				if rec.Header().Get("WWW-Authenticate") == "" {
					t.Error("missing WWW-Authenticate header")
				}
				decodeProblem(t, rec)
			}
		})
	}
}
