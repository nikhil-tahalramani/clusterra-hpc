package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// withEFSBase points the edge-agent's EFS root at a tempdir for the duration
// of one test. Mirrors the override pattern used by logs_test.go.
func withEFSBase(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("EFS_BASE_PATH", dir)
	return dir
}

func TestStageGitCreds_HappyPath(t *testing.T) {
	base := withEFSBase(t)

	body := []byte(`{"slot":"11111111-2222-3333-4444-555555555555","token":"ghs_test_xyz"}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/git-credentials", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	handleStageGitCreds(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%q want 201", rr.Code, rr.Body.String())
	}

	path := filepath.Join(base, "clusterra-system", "git-creds", "11111111-2222-3333-4444-555555555555.cred")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v want 0600", st.Mode().Perm())
	}
	got, _ := os.ReadFile(path)
	const want = "https://x-access-token:ghs_test_xyz@github.com\n"
	if string(got) != want {
		t.Fatalf("body=%q want %q", got, want)
	}

	// Parent dir must exist with 0700 (or tighter — MkdirAll honors umask but
	// not less-permissive existing modes; the smoke check is "owner-only").
	dirSt, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirSt.Mode().Perm()&0o077 != 0 {
		t.Fatalf("dir mode=%v leaks group/world perms", dirSt.Mode().Perm())
	}
}

func TestStageGitCreds_RejectsBadSlot(t *testing.T) {
	withEFSBase(t)
	cases := []struct {
		name, slot string
	}{
		{"path-traversal", "../../etc/passwd"},
		{"slash", "foo/bar"},
		{"empty", ""},
		{"not-uuid", "not-a-uuid"},
		{"trailing-dot", "11111111-2222-3333-4444-555555555555.."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := []byte(`{"slot":"` + c.slot + `","token":"ghs_test"}`)
			req := httptest.NewRequest(http.MethodPost, "/internal/agent/git-credentials", bytes.NewReader(body))
			rr := httptest.NewRecorder()
			handleStageGitCreds(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400 for slot %q", rr.Code, c.slot)
			}
		})
	}
}

func TestStageGitCreds_RejectsBadAuth(t *testing.T) {
	withEFSBase(t)
	t.Setenv("CLUSTERRA_AGENT_API_KEY", "secret-key")

	body := []byte(`{"slot":"11111111-2222-3333-4444-555555555555","token":"ghs_test"}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/git-credentials", bytes.NewReader(body))
	req.Header.Set("X-Agent-Key", "wrong-key")
	rr := httptest.NewRecorder()
	handleStageGitCreds(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rr.Code)
	}

	// Correct key passes.
	req2 := httptest.NewRequest(http.MethodPost, "/internal/agent/git-credentials", bytes.NewReader(body))
	req2.Header.Set("X-Agent-Key", "secret-key")
	rr2 := httptest.NewRecorder()
	handleStageGitCreds(rr2, req2)
	if rr2.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 with correct key", rr2.Code)
	}
}

func TestStageGitCreds_MalformedBody(t *testing.T) {
	withEFSBase(t)
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/git-credentials", bytes.NewReader([]byte(`{not json`)))
	rr := httptest.NewRecorder()
	handleStageGitCreds(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
}

func TestStageGitCreds_MissingToken(t *testing.T) {
	withEFSBase(t)
	body := []byte(`{"slot":"11111111-2222-3333-4444-555555555555"}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/git-credentials", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	handleStageGitCreds(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
}

func TestStageGitCreds_MethodNotAllowed(t *testing.T) {
	withEFSBase(t)
	req := httptest.NewRequest(http.MethodGet, "/internal/agent/git-credentials", nil)
	rr := httptest.NewRecorder()
	handleStageGitCreds(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rr.Code)
	}
}

// TestStageGitCreds_EFSWriteFailure forces the parent dir to be unwritable
// by pre-creating it with mode 0500. MkdirAll is then a no-op on the
// existing dir, and the subsequent OpenFile fails with EACCES.
//
// Skipped on root (UID 0) where DAC checks are bypassed and on Windows.
func TestStageGitCreds_EFSWriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only mode bits")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses DAC, can't simulate EACCES")
	}
	base := withEFSBase(t)

	dir := filepath.Join(base, "clusterra-system", "git-creds")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Tighten to read+execute only — MkdirAll on the existing dir is a
	// no-op (doesn't reapply mode) and the OpenFile that follows fails
	// with EACCES, exercising the write-failure error path.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	body := []byte(`{"slot":"11111111-2222-3333-4444-555555555555","token":"ghs_test"}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/git-credentials", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	handleStageGitCreds(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rr.Code)
	}
}

func TestStageGitCreds_RefusesDuplicateSlot(t *testing.T) {
	withEFSBase(t)
	body := []byte(`{"slot":"11111111-2222-3333-4444-555555555555","token":"ghs_test"}`)

	r1 := httptest.NewRequest(http.MethodPost, "/internal/agent/git-credentials", bytes.NewReader(body))
	rr1 := httptest.NewRecorder()
	handleStageGitCreds(rr1, r1)
	if rr1.Code != http.StatusCreated {
		t.Fatalf("first POST status=%d want 201", rr1.Code)
	}

	r2 := httptest.NewRequest(http.MethodPost, "/internal/agent/git-credentials", bytes.NewReader(body))
	rr2 := httptest.NewRecorder()
	handleStageGitCreds(rr2, r2)
	// Second write hits O_EXCL → 500 from writeGitCredsFile; the response
	// body MUST NOT mention the token.
	if rr2.Code != http.StatusInternalServerError {
		t.Fatalf("second POST status=%d want 500 (O_EXCL)", rr2.Code)
	}
	if bytes.Contains(rr2.Body.Bytes(), []byte("ghs_test")) {
		t.Fatal("token must not appear in error body")
	}
}
