package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// logBasePath is the directory the edge-agent reads Slurm stdout/stderr files
// from. On the customer control plane this is a hostPath mount of /mnt/efs.
func logBasePath() string {
	if v := os.Getenv("EFS_BASE_PATH"); v != "" {
		return v
	}
	return "/mnt/efs"
}

// logAuthKey is a shared secret required on log requests. When empty, auth is
// disabled (dev mode). Populated from CLUSTERRA_AGENT_API_KEY envFrom.
func logAuthKey() string {
	return os.Getenv("CLUSTERRA_AGENT_API_KEY")
}

func checkLogAuth(r *http.Request) bool {
	key := logAuthKey()
	if key == "" {
		return true
	}
	return r.Header.Get("X-Agent-Key") == key
}

// logFilePath resolves the on-disk path for a job's stdout/stderr file.
// Rejects any jobID containing path separators.
//
// Slurm writes `slurm-%j.out` relative to the job's working directory; the
// flat `/mnt/efs/slurm-%j.out` layout only holds when every template uses
// `current_working_directory: /mnt/efs`. Interactive templates (Jupyter) set
// a per-user CWD like `/mnt/efs/{email}`, which puts outputs one level down.
// Fall back to a glob against the user subdirectories — job IDs are unique
// per-cluster so collisions are impossible.
func logFilePath(jobID, stream string) (string, error) {
	if jobID == "" || strings.ContainsAny(jobID, "/\\") || strings.Contains(jobID, "..") {
		return "", fmt.Errorf("invalid job id")
	}
	ext := ".out"
	if stream == "stderr" {
		ext = ".err"
	}
	// Two naming conventions in the wild: Slurm's default `slurm-%j.out`
	// and the `job_%j.out` form some submissions set explicitly via
	// standard_output. Try both, flat then per-user-subdir glob.
	candidates := []string{
		fmt.Sprintf("slurm-%s%s", jobID, ext),
		fmt.Sprintf("job_%s%s", jobID, ext),
	}
	for _, name := range candidates {
		flat := filepath.Join(logBasePath(), name)
		if _, err := os.Stat(flat); err == nil {
			return flat, nil
		}
		matches, _ := filepath.Glob(filepath.Join(logBasePath(), "*", name))
		if len(matches) > 0 {
			return matches[0], nil
		}
	}
	return filepath.Join(logBasePath(), candidates[0]), nil
}

// handleGetLog serves GET /logs/{jobId}?stream=stdout|stderr — plain file read.
func handleGetLog(w http.ResponseWriter, r *http.Request) {
	if !checkLogAuth(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	jobID := r.PathValue("jobId")
	path, err := logFilePath(jobID, r.URL.Query().Get("stream"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "log not found", http.StatusNotFound)
			return
		}
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.Copy(w, f)
}

// handleStreamLog serves GET /logs/{jobId}/stream?stream=... — SSE tail-follow.
func handleStreamLog(w http.ResponseWriter, r *http.Request) {
	if !checkLogAuth(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	jobID := r.PathValue("jobId")
	path, err := logFilePath(jobID, r.URL.Query().Get("stream"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
		flusher.Flush()
		return
	}
	defer f.Close()

	// Seek back up to 64KB for initial context.
	const tailBytes = 64 * 1024
	if fi, err := f.Stat(); err == nil && fi.Size() > tailBytes {
		f.Seek(-tailBytes, io.SeekEnd)
	}

	buf := make([]byte, 4096)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		n, err := f.Read(buf)
		if n > 0 {
			writeSSEData(w, buf[:n])
			flusher.Flush()
		}
		if err != nil && err != io.EOF {
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
			flusher.Flush()
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// writeSSEData emits chunk bytes as an SSE "data:" event, splitting on newlines
// (SSE requires each data line be prefixed by "data: ").
func writeSSEData(w io.Writer, b []byte) {
	lines := strings.SplitAfter(string(b), "\n")
	fmt.Fprint(w, "data: ")
	for _, line := range lines {
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, "\n") {
			fmt.Fprint(w, strings.TrimSuffix(line, "\n"))
			fmt.Fprint(w, "\ndata: ")
		} else {
			fmt.Fprint(w, line)
		}
	}
	fmt.Fprint(w, "\n\n")
}
