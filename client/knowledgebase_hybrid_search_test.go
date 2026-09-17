package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHybridSearchSerializesChunkIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		var body struct {
			ChunkIDs []string `json:"chunk_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body.ChunkIDs) != 2 || body.ChunkIDs[0] != "chunk-1" || body.ChunkIDs[1] != "chunk-2" {
			t.Fatalf("chunk_ids = %#v", body.ChunkIDs)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, WithAPIKey("sk-test"))
	_, err := c.HybridSearch(context.Background(), "kb-1", &SearchParams{
		QueryText: "query",
		ChunkIDs:  []string{"chunk-1", "chunk-2"},
	})
	if err != nil {
		t.Fatalf("HybridSearch() error = %v", err)
	}
}

func TestSearchParamsOmitsEmptyChunkIDs(t *testing.T) {
	body, err := json.Marshal(SearchParams{QueryText: "query"})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, exists := decoded["chunk_ids"]; exists {
		t.Fatalf("empty chunk_ids must be omitted: %s", body)
	}
}
