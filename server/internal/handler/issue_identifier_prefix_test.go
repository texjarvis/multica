package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGetIssueRejectsForeignIdentifierPrefix guards against resolveIssueByIdentifier
// resolving "PREFIX-NUMBER" purely on the number, which let any foreign prefix
// (e.g. "ZZZ-500") silently return the current workspace's issue with that
// number (VIS-12650).
func TestGetIssueRejectsForeignIdentifierPrefix(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("requires DB")
	}

	ctx := context.Background()
	setWorkspaceIssuePrefixForTest(t, "MUL")

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, number)
		VALUES ($1, 'member', $2, $3, 9042)
		RETURNING id
	`, testWorkspaceID, testUserID, "identifier prefix must match workspace").Scan(&issueID); err != nil {
		t.Fatalf("create issue fixture: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	// The real workspace prefix ("MUL") resolves normally.
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues/MUL-9042", nil)
	req = withURLParam(req, "id", "MUL-9042")
	testHandler.GetIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetIssue(MUL-9042): expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode GetIssue response: %v", err)
	}
	if resp.ID != issueID {
		t.Fatalf("GetIssue(MUL-9042): expected id %s, got %s", issueID, resp.ID)
	}

	// A foreign prefix with the same number must not resolve to this issue.
	for _, foreign := range []string{"ZZZ-9042", "FOO-9042", "VIS-9042"} {
		w := httptest.NewRecorder()
		req := newRequest("GET", "/api/issues/"+foreign, nil)
		req = withURLParam(req, "id", foreign)
		testHandler.GetIssue(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("GetIssue(%s): expected 404 for foreign prefix, got %d: %s", foreign, w.Code, w.Body.String())
		}
	}
}
