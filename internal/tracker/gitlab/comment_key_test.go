package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/tracker"
)

func glNote(id int, body string) map[string]any {
	return map[string]any{
		"id": float64(id), "body": body, "created_at": "2024-01-01T00:00:00Z",
		"author": map[string]any{"id": float64(7), "username": "itervox-bot"}, "system": false,
	}
}

func TestCreateCommentWithKeyEmbedsHiddenMarker(t *testing.T) {
	var gotBody map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/projects/group%2Fproject/issues/5/notes", r.URL.EscapedPath())
		assert.Equal(t, http.MethodPost, r.Method)
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(glNote(99, gotBody["body"]))
	}))
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	comment, err := client.CreateCommentWithKey(context.Background(), "5", "key-1", "question?")
	require.NoError(t, err)
	assert.Equal(t, tracker.MarkCommentKey("question?", "key-1"), gotBody["body"])
	assert.True(t, tracker.CommentHasKey(comment.Body, "key-1"))
	assert.Equal(t, "99", comment.ID)
}

func TestFindCommentByKeyScansNewestFirst(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/projects/group%2Fproject/issues/5/notes", r.URL.EscapedPath())
		assert.Equal(t, "desc", r.URL.Query().Get("sort"))
		assert.Equal(t, "created_at", r.URL.Query().Get("order_by"))
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]any{
			glNote(3, "human reply"),
			glNote(2, tracker.MarkCommentKey("new question", "key-2")),
			glNote(1, tracker.MarkCommentKey("old question", "key-1")),
		})
	}))
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	comment, found, err := client.FindCommentByKey(context.Background(), "5", "key-2")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "2", comment.ID)
	assert.Equal(t, "7", comment.AuthorID)
	assert.Equal(t, "itervox-bot", comment.AuthorName)
	require.NotNil(t, comment.CreatedAt)
}

func TestFindCommentByKeyFollowsNextLink(t *testing.T) {
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			w.Header().Set("Link", fmt.Sprintf(`<%s/x?page=1>; rel="prev", <%s/x?page=2>; rel="last"`, ts.URL, ts.URL))
			_ = json.NewEncoder(w).Encode([]any{glNote(1, tracker.MarkCommentKey("q", "key-1"))})
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=2>; rel="next"`, ts.URL, r.URL.EscapedPath()))
		_ = json.NewEncoder(w).Encode([]any{glNote(2, "unrelated")})
	}))
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	comment, found, err := client.FindCommentByKey(context.Background(), "5", "key-1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "1", comment.ID)
}

func TestFindCommentByKeyAbsentWhenThreadExhausted(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GitLab sends first/last on the final page but no rel="next".
		w.Header().Set("Link", `<https://gitlab.example/x?page=1>; rel="first", <https://gitlab.example/x?page=1>; rel="last"`)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]any{glNote(1, tracker.MarkCommentKey("q", "other-key"))})
	}))
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	comment, found, err := client.FindCommentByKey(context.Background(), "5", "key-1")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, comment)
}

func TestFindCommentByKeyBoundsPageScan(t *testing.T) {
	requests := 0
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=%d>; rel="next"`, ts.URL, r.URL.EscapedPath(), requests+1))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]any{glNote(requests, "page "+strconv.Itoa(requests))})
	}))
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	_, found, err := client.FindCommentByKey(context.Background(), "5", "key-1")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, gitlabKeyScanPages, requests)
}

func TestFindCommentByKeyErrorsWhenAnswerUnknown(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"server error": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"not found":    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
		"bad json":     func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("[{")) },
		"not a list":   func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"message":"x"}`)) },
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			ts := httptest.NewServer(handler)
			defer ts.Close()

			client := newTestClient(ts.URL, defaultTestConfig())
			_, found, err := client.FindCommentByKey(context.Background(), "5", "key-1")
			require.Error(t, err)
			assert.False(t, found)
		})
	}
}
