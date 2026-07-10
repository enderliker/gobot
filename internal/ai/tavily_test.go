package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCallTavilySearchSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST request, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected application/json content type, got %s", r.Header.Get("Content-Type"))
		}

		var req TavilySearchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		if req.APIKey != "test-key" {
			t.Errorf("expected api_key test-key, got %s", req.APIKey)
		}
		if req.Query != "go 1.25 release date" {
			t.Errorf("expected query 'go 1.25 release date', got %s", req.Query)
		}

		resp := TavilySearchResponse{
			Results: []TavilySearchResult{
				{
					Title:   "Go 1.25 Release Notes",
					URL:     "https://go.dev/doc/go1.25",
					Content: "Go 1.25 is released in early 2026.",
					Score:   0.99,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	oldURL := tavilySearchURL
	tavilySearchURL = ts.URL
	defer func() { tavilySearchURL = oldURL }()

	t.Setenv("TAVILY_API_KEY", "test-key")

	results, err := CallTavilySearch(context.Background(), "go 1.25 release date")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Title != "Go 1.25 Release Notes" {
		t.Errorf("expected title 'Go 1.25 Release Notes', got %s", results[0].Title)
	}
	if results[0].URL != "https://go.dev/doc/go1.25" {
		t.Errorf("expected URL 'https://go.dev/doc/go1.25', got %s", results[0].URL)
	}
	if results[0].Content != "Go 1.25 is released in early 2026." {
		t.Errorf("expected content 'Go 1.25 is released in early 2026.', got %s", results[0].Content)
	}
}

func TestCallTavilySearchMissingAPIKey(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "")
	_, err := CallTavilySearch(context.Background(), "some query")
	if err == nil {
		t.Fatalf("expected error due to missing API key, got nil")
	}
}

func TestParseWebSearchTool(t *testing.T) {
	call, err := ParseToolArgumentsJSON("WEB_SEARCH", `{"query":"what is golang"}`)
	if err != nil {
		t.Fatalf("expected valid web_search parse, got error: %v", err)
	}

	if call.Tool != "web_search" {
		t.Errorf("expected tool to be web_search, got %s", call.Tool)
	}
	if call.Query != "what is golang" {
		t.Errorf("expected query 'what is golang', got %s", call.Query)
	}
	if got, want := call.Confirmation, "Perform a web search for `what is golang`?"; got != want {
		t.Errorf("expected confirmation %q, got %q", want, got)
	}
}

func TestParseWebSearchMissingQuery(t *testing.T) {
	_, err := ParseToolArgumentsJSON("web_search", `{"query":""}`)
	if err == nil {
		t.Fatalf("expected validation error for empty query, got nil")
	}
}
