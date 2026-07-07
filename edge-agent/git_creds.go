package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

// credsBasePath is the EFS subdirectory the edge-agent writes per-job
// credential bundles into. The slurmd Prolog (images/clusterra-slurmd/prolog.sh)
// reads from the same path, copies the file into per-job scratch as
// `.git-credentials`, chmods to the job UID, and then the Epilog removes the
// source file. Token never lands in the rendered script, the job environment,
// any DDB row, or any log line.
//
// Tests override via the EFS_BASE_PATH env var (reuses the existing knob used
// by logs.go::logBasePath) — there is no separate package-level var.
func credsBasePath() string {
	return filepath.Join(logBasePath(), "clusterra-system", "git-creds")
}

// slotPattern restricts slot ids to UUIDs (with or without hyphens). Defensive:
// the slot lands in a filesystem path, so refusing anything outside a fixed
// alphabet prevents traversal (`..`), separator injection (`/`), or empty
// names. Cluster-api always sends a fresh uuid.NewString() — anything else is
// a client bug or an attack.
var slotPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}$`)

// gitCredsRequest is the wire shape for POST /internal/agent/git-credentials.
// Both fields required; both validated before any FS work.
type gitCredsRequest struct {
	Slot  string `json:"slot"`
	Token string `json:"token"`
}

// handleStageGitCreds writes a one-line netrc-style credentials file to
// <EFS>/clusterra-system/git-creds/<slot>.cred with mode 0600. Creates the
// parent directory on first call with mode 0700.
//
// Auth is shared with the log endpoints (X-Agent-Key against
// CLUSTERRA_AGENT_API_KEY) so the edge-agent retains a single auth surface.
// When the key is empty we accept the request — mirrors the log endpoints'
// dev-mode behavior; production always sets the key.
//
// Responses:
//
//	201 Created          — file written
//	400 Bad Request      — malformed body / missing or non-UUID slot / empty token
//	401 Unauthorized     — bad X-Agent-Key
//	405 Method Not Allowed
//	500 Internal Error   — EFS write failed
//
// IMPORTANT: the token MUST NOT be logged. Audit logging records only (slot, ts).
func handleStageGitCreds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !checkLogAuth(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Cap the body size — the legitimate payload is well under 1 KiB
	// (slot + a short install token). Anything larger is either malformed
	// or an attempt to write large junk into EFS via the JSON decoder.
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var req gitCredsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "malformed body", http.StatusBadRequest)
		return
	}
	if req.Slot == "" || !slotPattern.MatchString(req.Slot) {
		http.Error(w, "invalid slot", http.StatusBadRequest)
		return
	}
	if req.Token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	if err := writeGitCredsFile(req.Slot, req.Token); err != nil {
		// Never echo err back to the caller: even though the underlying
		// errors from os.* don't carry the token, defense in depth.
		slog.Error("git-credentials: write failed", "slot", req.Slot, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}

	slog.Info("git-credentials: staged", "slot", req.Slot)
	w.WriteHeader(http.StatusCreated)
}

// writeGitCredsFile materializes the netrc-style credential file. Split from
// the HTTP handler so unit tests can exercise the FS path without a server.
//
// Idempotent on the directory (MkdirAll), strict on the file: we use O_EXCL
// to refuse to overwrite an existing slot — a duplicate POST on the same
// slot is either a retry storm (the client should mint a new slot) or
// adversarial behavior. The cluster-api uses fresh UUIDs per submit so
// collisions are impossible under correct usage.
func writeGitCredsFile(slot, token string) error {
	dir := credsBasePath()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir creds root: %w", err)
	}
	path := filepath.Join(dir, slot+".cred")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("open creds file: %w", err)
	}
	defer f.Close()
	body := fmt.Sprintf("https://x-access-token:%s@github.com\n", token)
	if _, err := f.WriteString(body); err != nil {
		// Partial write: best-effort delete so a half-written file with a
		// truncated token can't be parsed by the Prolog.
		_ = os.Remove(path)
		return fmt.Errorf("write creds: %w", err)
	}
	return nil
}
