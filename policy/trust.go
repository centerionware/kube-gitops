package policy

import (
	"strings"

	"kube-gitops/api"
	"kube-gitops/webhook"
)

// defaultAllowedAssociations is used when prPolicy.allowedAuthorAssociations
// is not specified on the GitRepo.
var defaultAllowedAssociations = []string{"OWNER", "MEMBER", "COLLABORATOR"}

// EvaluatePR decides whether a PR event should trigger a deployment.
// Returns true if the event passes all configured policy gates.
// Silent false means ignore — no error, no noise.
func EvaluatePR(policy api.PRPolicySpec, event webhook.PREvent) bool {
	// Comment trigger path — evaluated separately from PR open/sync
	if event.Action == "comment" {
		return evaluateComment(policy, event)
	}

	// Check author association
	if !associationAllowed(policy, event.AuthorAssociation) {
		return false
	}

	// Check required label if configured
	if policy.RequireLabel != "" {
		if !hasLabel(event.Labels, policy.RequireLabel) {
			return false
		}
	}

	return true
}

// EvaluateSync decides whether a PR synchronize event (new push to PR branch)
// should trigger a rebuild. Same trust rules apply — if the original PR was
// trusted, the push is too.
func EvaluateSync(policy api.PRPolicySpec, event webhook.PREvent) bool {
	return EvaluatePR(policy, event)
}

// evaluateComment evaluates a comment event against the comment trigger policy.
func evaluateComment(policy api.PRPolicySpec, event webhook.PREvent) bool {
	if !policy.AllowCommentTrigger {
		return false
	}

	phrase := policy.CommentTriggerPhrase
	if phrase == "" {
		phrase = "/deploy"
	}

	// Comment must contain the trigger phrase
	if !strings.Contains(event.CommentBody, phrase) {
		return false
	}

	// Check explicit allowedCommenters list first
	if len(policy.AllowedCommenters) > 0 {
		for _, allowed := range policy.AllowedCommenters {
			if allowed == "*" || strings.EqualFold(allowed, event.CommentAuthor) {
				return true
			}
		}
		return false
	}

	// Fall back to association check on the commenter
	return associationAllowed(policy, event.CommentAuthorAssociation)
}

// associationAllowed checks whether the given author association is in the
// allowed list, using the default list if none is configured.
func associationAllowed(policy api.PRPolicySpec, association string) bool {
	allowed := policy.AllowedAuthorAssociations
	if len(allowed) == 0 {
		allowed = defaultAllowedAssociations
	}
	for _, a := range allowed {
		if strings.EqualFold(a, association) {
			return true
		}
	}
	return false
}

// hasLabel reports whether the given label name is present in the label list.
func hasLabel(labels []string, label string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, label) {
			return true
		}
	}
	return false
}
