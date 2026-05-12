package webhook

import (
	"fmt"
	"net/http"
)

// parsePlatformEvent dispatches raw webhook body to the correct platform parser.
// Returns the normalized PREvent, a skip bool (true = not a PR event, ignore),
// and any parse error.
func parsePlatformEvent(platform string, r *http.Request, body []byte) (PREvent, bool, error) {
	switch platform {
	case "github":
		return parseGitHub(r, body)
	case "gitlab":
		return parseGitLab(r, body)
	case "gitea", "forgejo":
		return parseGitea(r, body)
	default:
		return PREvent{}, false, fmt.Errorf("unsupported platform: %s", platform)
	}
}
