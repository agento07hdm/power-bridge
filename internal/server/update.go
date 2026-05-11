package server

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"time"
)

// updateCheckResponse is the JSON response for GET /api/update/check.
type updateCheckResponse struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	ReleaseURL      string `json:"release_url"`
}

// apiUpdateCheck fetches the latest release from GitHub and reports whether an
// update is available. Non-critical errors (network issues, etc.) result in an
// empty LatestVersion rather than an HTTP error so the UI degrades gracefully.
func (s *Server) apiUpdateCheck(w http.ResponseWriter, r *http.Request) {
	jsonHeader(w)
	resp := updateCheckResponse{CurrentVersion: Version}

	client := &http.Client{Timeout: 8 * time.Second}
	ghResp, err := client.Get("https://api.github.com/repos/fedzzito/power-bridge/releases/latest")
	if err != nil {
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	defer ghResp.Body.Close()

	var ghData struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(ghResp.Body).Decode(&ghData); err != nil {
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	resp.LatestVersion = ghData.TagName
	resp.ReleaseURL = ghData.HTMLURL
	// Only report update_available when both versions are known and differ;
	// skip check for dev builds to avoid spurious update notifications during
	// development.
	resp.UpdateAvailable = ghData.TagName != "" && ghData.TagName != Version && Version != "dev"
	_ = json.NewEncoder(w).Encode(resp)
}

// apiUpdateApply triggers the update.sh script and restarts the service.
// It returns immediately – the actual update happens in the background.
func (s *Server) apiUpdateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	jsonHeader(w)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "update_started"})
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = exec.Command("bash", "/usr/local/share/power-bridge/update.sh").Run()
		_ = exec.Command("systemctl", "restart", serviceName).Run()
	}()
}
