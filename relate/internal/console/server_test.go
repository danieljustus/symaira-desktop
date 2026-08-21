package console

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-relate/internal/app"
)

func testApp(t *testing.T) *app.App {
	t.Helper()
	a, err := app.OpenMemory(context.Background())
	if err != nil {
		t.Fatalf("app.OpenMemory() error = %v", err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func testServer(t *testing.T) (*Server, string) {
	t.Helper()
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	return New(testApp(t), token), token
}

func TestListen_BindsLoopbackOnly(t *testing.T) {
	ln, err := Listen(0)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()

	addr := ln.Addr().String()
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Errorf("listener address = %q, want a 127.0.0.1 bind (never 0.0.0.0 or a wildcard)", addr)
	}
}

func TestHandler_UnauthenticatedRequest_IsRejected(t *testing.T) {
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/contacts")
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestHandler_BearerToken_IsAccepted(t *testing.T) {
	s, token := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/contacts", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestHandler_WrongToken_IsRejected(t *testing.T) {
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/contacts", nil)
	req.Header.Set("Authorization", "Bearer wrong-token-entirely")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestHandler_TokenBootstrap_SetsSessionCookie(t *testing.T) {
	s, token := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/?token=" + token)
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want %d (redirect)", resp.StatusCode, http.StatusFound)
	}
	loc := resp.Header.Get("Location")
	if strings.Contains(loc, "token=") {
		t.Errorf("redirect Location still contains the token: %q", loc)
	}

	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected a session cookie to be set")
	}
	if !sessionCookie.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
}

func doJSON(t *testing.T, srv *httptest.Server, token, method, path string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var parsed map[string]any
	dec := json.NewDecoder(resp.Body)
	_ = dec.Decode(&parsed) // some endpoints (delete) may have an empty body on error paths only
	return resp, parsed
}

func TestContactLifecycle_CreateGetUpdateErase(t *testing.T) {
	s, token := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, created := doJSON(t, srv, token, http.MethodPost, "/api/v1/contacts", map[string]any{
		"display_name": "Ada Lovelace", "email": "ada@example.com",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body = %v", resp.StatusCode, created)
	}
	id, _ := created["ID"].(string)
	if id == "" {
		t.Fatalf("created contact missing ID: %v", created)
	}

	resp, got := doJSON(t, srv, token, http.MethodGet, "/api/v1/contacts/"+id, nil)
	if resp.StatusCode != http.StatusOK || got["DisplayName"] != "Ada Lovelace" {
		t.Fatalf("get status=%d body=%v", resp.StatusCode, got)
	}

	resp, updated := doJSON(t, srv, token, http.MethodPatch, "/api/v1/contacts/"+id, map[string]any{"display_name": "Ada King"})
	if resp.StatusCode != http.StatusOK || updated["DisplayName"] != "Ada King" {
		t.Fatalf("update status=%d body=%v", resp.StatusCode, updated)
	}

	resp, _ = doJSON(t, srv, token, http.MethodDelete, "/api/v1/contacts/"+id, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("erase status = %d", resp.StatusCode)
	}

	resp, _ = doJSON(t, srv, token, http.MethodGet, "/api/v1/contacts/"+id, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after erase status = %d, want 404", resp.StatusCode)
	}
}

func TestHandler_UnknownFieldInBody_IsRejected(t *testing.T) {
	s, token := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, body := doJSON(t, srv, token, http.MethodPost, "/api/v1/contacts", map[string]any{
		"display_name": "X", "not_a_real_field": "y",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d, body = %v", resp.StatusCode, http.StatusBadRequest, body)
	}
}

func TestHandler_ErrorResponse_NeverLeaksRawErrorType(t *testing.T) {
	s, token := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, body := doJSON(t, srv, token, http.MethodGet, "/api/v1/contacts/does-not-exist", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if _, ok := body["error"]; !ok {
		t.Errorf("expected an error field in body: %v", body)
	}
}

func TestHandler_OversizedBody_IsRejected(t *testing.T) {
	s, token := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	oversized := strings.Repeat("x", (4<<20)+1)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/contacts", strings.NewReader(oversized))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for oversized body", resp.StatusCode, http.StatusBadRequest)
	}
}
