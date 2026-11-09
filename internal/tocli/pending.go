package tocli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// A pending marker records that a session's first prompt already has a node.
// The prompt-submit hook consumes it once, so that prompt is not recorded
// twice while later prompts in the same session still get their own nodes.

func pendingPath(baseDir, nodeID string) (string, error) {
	id, err := uuid.Parse(nodeID)
	if err != nil {
		return "", fmt.Errorf("node id tidak valid: %q", nodeID)
	}
	return filepath.Join(baseDir, "tocli-pending", id.String()), nil
}

// MarkPending records that nodeID is waiting for its hook call.
func MarkPending(baseDir, nodeID string) error {
	path, err := pendingPath(baseDir, nodeID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, nil, 0o600)
}

// ClaimPending reports whether this call is the first to claim nodeID.
func ClaimPending(baseDir, nodeID string) bool {
	path, err := pendingPath(baseDir, nodeID)
	if err != nil {
		return false
	}
	return os.Remove(path) == nil
}

// ClearPending removes a marker the hook never consumed.
func ClearPending(baseDir, nodeID string) {
	ClaimPending(baseDir, nodeID)
}

// StripMarkers removes attachment markers from typed text.
func StripMarkers(text string, attachments []Attachment) string {
	for _, a := range attachments {
		text = strings.ReplaceAll(text, a.Marker, "")
	}
	return text
}

var repeatedSpace = regexp.MustCompile(`[ \t]+`)

// Title makes a journey title from typed text, ignoring attachments.
func Title(typed string, attachments []Attachment) string {
	for _, line := range strings.Split(StripMarkers(typed, attachments), "\n") {
		if line = strings.TrimSpace(repeatedSpace.ReplaceAllString(line, " ")); line != "" {
			return line
		}
	}
	return "(hanya attachment)"
}
