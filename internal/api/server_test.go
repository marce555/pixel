package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestAuthMiddleware(t *testing.T) {
	srv := &Server{}
	handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	os.Setenv("PIXEL_API_TOKEN", "secret_freebox_token_123")
	defer os.Unsetenv("PIXEL_API_TOKEN")

	// 1. Localhost request should succeed without token
	reqLocal := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	reqLocal.RemoteAddr = "127.0.0.1:54321"
	recLocal := httptest.NewRecorder()
	handler.ServeHTTP(recLocal, reqLocal)
	if recLocal.Code != http.StatusOK {
		t.Fatalf("expected 200 for localhost without token, got %d", recLocal.Code)
	}

	// 2. External IP without token should fail with 401
	reqExtNoToken := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	reqExtNoToken.RemoteAddr = "82.64.12.34:54321"
	recExtNoToken := httptest.NewRecorder()
	handler.ServeHTTP(recExtNoToken, reqExtNoToken)
	if recExtNoToken.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for external IP without token, got %d", recExtNoToken.Code)
	}

	// 3. External IP with wrong token should fail with 401
	reqExtWrong := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	reqExtWrong.RemoteAddr = "82.64.12.34:54321"
	reqExtWrong.Header.Set("Authorization", "Bearer wrong_token")
	recExtWrong := httptest.NewRecorder()
	handler.ServeHTTP(recExtWrong, reqExtWrong)
	if recExtWrong.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for external IP with wrong token, got %d", recExtWrong.Code)
	}

	// 4. External IP with valid Bearer token should succeed with 200
	reqExtBearer := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	reqExtBearer.RemoteAddr = "82.64.12.34:54321"
	reqExtBearer.Header.Set("Authorization", "Bearer secret_freebox_token_123")
	recExtBearer := httptest.NewRecorder()
	handler.ServeHTTP(recExtBearer, reqExtBearer)
	if recExtBearer.Code != http.StatusOK {
		t.Fatalf("expected 200 for external IP with valid Bearer token, got %d", recExtBearer.Code)
	}

	// 5. External IP with valid query token should succeed with 200
	reqExtQuery := httptest.NewRequest(http.MethodGet, "/api/events?token=secret_freebox_token_123", nil)
	reqExtQuery.RemoteAddr = "82.64.12.34:54321"
	recExtQuery := httptest.NewRecorder()
	handler.ServeHTTP(recExtQuery, reqExtQuery)
	if recExtQuery.Code != http.StatusOK {
		t.Fatalf("expected 200 for external IP with valid query token, got %d", recExtQuery.Code)
	}
}
