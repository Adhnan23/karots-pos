package aicatalog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIdentifyParsesChatCompletion(t *testing.T) {
	// Fake OpenAI-compatible server that echoes a canned identify JSON as the
	// assistant message content.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("missing bearer, got %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "6200 2rs") {
			t.Errorf("query not forwarded: %s", body)
		}
		content := `{"confident":true,"best":{"name":"Ball Bearing 6200 2RS","category":"Bearings/Deep Groove","specs":"10x30x9","explanation":"sealed"},"options":[]}`
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": content}}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(Settings{BaseURL: srv.URL, Model: "x", APIKey: "test-key"})
	got, err := c.Identify(context.Background(), "6200 2rs", "", nil)
	if err != nil {
		t.Fatalf("Identify err: %v", err)
	}
	if !got.Confident || got.Best.Category != "Bearings/Deep Groove" {
		t.Fatalf("bad result: %+v", got)
	}
}

func TestIdentifyGeminiUsesGroundingEndpoint(t *testing.T) {
	// Fake Gemini native endpoint: assert we hit generateContent with the
	// google_search tool and the key as a query param, and parse candidates.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/models/gemini-2.5-flash:generateContent") {
			t.Errorf("wrong path %s", r.URL.Path)
		}
		if r.URL.Query().Get("key") != "gkey" {
			t.Errorf("missing key query param, got %q", r.URL.RawQuery)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "google_search") {
			t.Errorf("grounding tool not requested: %s", body)
		}
		content := `{"confident":true,"best":{"name":"Bajaj RE Drawer Lock","category":"Automotive > Three-Wheeler > Locks","specs":"","explanation":"OEM lock"},"options":[]}`
		resp := map[string]any{"candidates": []map[string]any{{"content": map[string]any{"parts": []map[string]any{{"text": content}}}}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(Settings{Provider: "gemini", BaseURL: srv.URL, Model: "gemini-2.5-flash", APIKey: "gkey"})
	got, err := c.Identify(context.Background(), "bajaj drawer lock", "", nil)
	if err != nil {
		t.Fatalf("Identify err: %v", err)
	}
	if !got.Confident || got.Best.Category != "Automotive > Three-Wheeler > Locks" {
		t.Fatalf("bad result: %+v", got)
	}
}

func TestIdentifyErrorsWithoutKey(t *testing.T) {
	c := NewClient(Settings{BaseURL: "http://example.invalid", Model: "x", APIKey: ""})
	if _, err := c.Identify(context.Background(), "anything", "", nil); err == nil {
		t.Fatal("expected an error when no API key is set")
	}
}
