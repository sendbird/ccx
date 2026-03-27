package remote

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SavedSession is the persistable state of a remote session.
type SavedSession struct {
	PodName   string `yaml:"pod_name"`
	Context   string `yaml:"context"`
	Namespace string `yaml:"namespace"`
	Image     string `yaml:"image"`
	LocalDir  string `yaml:"local_dir,omitempty"`
	SessionID string `yaml:"session_id,omitempty"`
	WorkDir   string `yaml:"work_dir"`
	Status    string `yaml:"status"` // running, stopped, failed
}

func remoteSessPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ccx", "remote-sessions.yaml")
}

// LoadSavedSessions reads persisted remote sessions from disk.
func LoadSavedSessions() []SavedSession {
	data, err := os.ReadFile(remoteSessPath())
	if err != nil {
		return nil
	}
	var sessions []SavedSession
	yaml.Unmarshal(data, &sessions)
	return sessions
}

// SaveSessions writes remote sessions to disk.
func SaveSessions(sessions []SavedSession) {
	path := remoteSessPath()
	os.MkdirAll(filepath.Dir(path), 0755)
	data, err := yaml.Marshal(sessions)
	if err != nil {
		return
	}
	os.WriteFile(path, data, 0644)
}

// AddSavedSession appends a session and saves.
func AddSavedSession(s SavedSession) {
	sessions := LoadSavedSessions()
	// Replace if same pod name exists
	found := false
	for i, existing := range sessions {
		if existing.PodName == s.PodName {
			sessions[i] = s
			found = true
			break
		}
	}
	if !found {
		sessions = append(sessions, s)
	}
	SaveSessions(sessions)
}

// UpdateSavedSessionStatus updates the status of a saved session.
func UpdateSavedSessionStatus(podName, status string) {
	sessions := LoadSavedSessions()
	for i, s := range sessions {
		if s.PodName == podName {
			sessions[i].Status = status
			break
		}
	}
	SaveSessions(sessions)
}

// RemoveSavedSession removes a session by pod name.
func RemoveSavedSession(podName string) {
	sessions := LoadSavedSessions()
	var filtered []SavedSession
	for _, s := range sessions {
		if s.PodName != podName {
			filtered = append(filtered, s)
		}
	}
	SaveSessions(filtered)
}
