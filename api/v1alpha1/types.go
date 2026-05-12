package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{
	Group:   "kube-gitops.centerionware.app",
	Version: "v1alpha1",
}

// ----------------------------------------------------------------
// GitRepo — defines a monitored git repository
// ----------------------------------------------------------------

type GitRepo struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GitRepoSpec   `json:"spec,omitempty"`
	Status GitRepoStatus `json:"status,omitempty"`
}

type GitRepoList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GitRepo `json:"items"`
}

func (in *GitRepo) DeepCopyObject() runtime.Object {
	out := new(GitRepo)
	*out = *in
	return out
}

func (in *GitRepoList) DeepCopyObject() runtime.Object {
	out := new(GitRepoList)
	*out = *in
	return out
}

type GitRepoSpec struct {
	// Platform identifies the git hosting platform.
	// forgejo is treated as gitea (identical API).
	// +kubebuilder:validation:Enum=github;gitlab;gitea;forgejo
	Platform string `json:"platform"`

	// Repo is the full HTTPS URL of the repository.
	// e.g. https://github.com/org/myapp
	Repo string `json:"repo"`

	// GitSecret is the name of a Kubernetes Secret in the same namespace
	// containing platform credentials. Supports two formats:
	//   HTTPS/token: username + password (token) keys
	//   SSH:         ssh-privatekey key
	// This secret is also forwarded to the generated kube-deploy App CR
	// so it can clone the PR branch during the build.
	GitSecret string `json:"gitSecret"`

	// Trigger defines how the operator detects new pull requests.
	Trigger TriggerSpec `json:"trigger"`

	// PRPolicy defines who is allowed to trigger a PR deployment.
	// Requests from untrusted actors are silently ignored.
	PRPolicy PRPolicySpec `json:"prPolicy,omitempty"`

	// PRDeploy defines the kube-deploy App template to use when
	// generating deployments for pull requests.
	PRDeploy PRDeploySpec `json:"prDeploy,omitempty"`
}

// ----------------------------------------------------------------
// TRIGGER
// ----------------------------------------------------------------

type TriggerSpec struct {
	// Mode selects the trigger mechanism.
	// webhook: the git platform sends events to our ingress endpoint (preferred).
	// poll:    we periodically query the platform API for open PRs.
	// +kubebuilder:validation:Enum=webhook;poll
	Mode string `json:"mode"`

	// PollInterval is how often to query the platform API when mode=poll.
	// Accepts Go duration strings e.g. "2m", "30s". Default: "2m".
	// +optional
	PollInterval string `json:"pollInterval,omitempty"`

	// WebhookSecret is the name of a Kubernetes Secret containing the
	// shared secret used to validate incoming webhook payloads (HMAC-SHA256).
	// Required when mode=webhook.
	// The Secret must have a key named "secret".
	// +optional
	WebhookSecret string `json:"webhookSecret,omitempty"`

	// WebhookPath is the HTTP path this repo's webhook listens on.
	// Defaults to /webhook/<namespace>/<name>.
	// Must be unique across all GitRepo objects in the cluster.
	// +optional
	WebhookPath string `json:"webhookPath,omitempty"`
}

// ----------------------------------------------------------------
// PR POLICY — trust model
// ----------------------------------------------------------------

type PRPolicySpec struct {
	// AllowedAuthorAssociations lists the platform-reported author association
	// levels that are permitted to trigger a deployment automatically.
	//
	// GitHub values: OWNER, MEMBER, COLLABORATOR, CONTRIBUTOR, FIRST_TIMER,
	//                FIRST_TIME_CONTRIBUTOR, MANNEQUIN, NONE
	// GitLab/Gitea:  mapped to equivalent values by the platform adapter.
	//
	// Defaults to ["OWNER", "MEMBER", "COLLABORATOR"] if not set.
	// +optional
	AllowedAuthorAssociations []string `json:"allowedAuthorAssociations,omitempty"`

	// RequireLabel, if set, means a PR must carry this label before a
	// deployment is triggered. Useful as a manual gate for external contributors.
	// e.g. "deploy-preview"
	// +optional
	RequireLabel string `json:"requireLabel,omitempty"`

	// AllowCommentTrigger enables triggering a deployment by posting a
	// comment containing the value of CommentTriggerPhrase on an open PR.
	// The commenter must satisfy AllowedAuthorAssociations.
	// +optional
	AllowCommentTrigger bool `json:"allowCommentTrigger,omitempty"`

	// CommentTriggerPhrase is the exact string that must appear in a comment
	// to trigger a deployment. Defaults to "/deploy".
	// +optional
	CommentTriggerPhrase string `json:"commentTriggerPhrase,omitempty"`

	// AllowedCommenters is an optional explicit list of usernames allowed to
	// use the comment trigger, regardless of their association level.
	// Use ["*"] to allow any repository member.
	// +optional
	AllowedCommenters []string `json:"allowedCommenters,omitempty"`
}

// ----------------------------------------------------------------
// PR DEPLOY — kube-deploy App template
// ----------------------------------------------------------------

type PRDeploySpec struct {
	// Namespace to create kube-deploy App CRs in.
	// Defaults to the GitRepo's own namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// NameTemplate is a Go template for the App CR name.
	// Available variables: .RepoName .PRNumber .BranchSlug
	// Default: "{{.RepoName}}-pr-{{.PRNumber}}"
	// +optional
	NameTemplate string `json:"nameTemplate,omitempty"`

	// IngressHostTemplate is a Go template for the ingress hostname.
	// Available variables: .RepoName .PRNumber .BranchSlug .BaseDomain
	// Default: "pr-{{.PRNumber}}.{{.RepoName}}.{{.BaseDomain}}"
	// +optional
	IngressHostTemplate string `json:"ingressHostTemplate,omitempty"`

	// BaseDomain is substituted into IngressHostTemplate as .BaseDomain.
	// +optional
	BaseDomain string `json:"baseDomain,omitempty"`

	// InjectPREnv, when true (default), injects PR metadata as environment
	// variables into the generated App:
	//   GITOPS_PR_NUMBER, GITOPS_PR_BRANCH, GITOPS_PR_SHA,
	//   GITOPS_PR_AUTHOR, GITOPS_REPO_URL
	// +optional
	InjectPREnv *bool `json:"injectPREnv,omitempty"`

	// Build overrides forwarded verbatim to the generated kube-deploy App's
	// spec.build block. gitSecret is always set from the GitRepo's GitSecret.
	// +optional
	Build PRBuildOverrides `json:"build,omitempty"`

	// Run overrides forwarded verbatim to the generated kube-deploy App's
	// spec.run block.
	// +optional
	Run PRRunOverrides `json:"run,omitempty"`

	// Ingress configures the ingress block on the generated App CR.
	// +optional
	Ingress *PRIngressOverrides `json:"ingress,omitempty"`

	// Gateway configures the gateway block on the generated App CR
	// (mutually exclusive with Ingress).
	// +optional
	Gateway *PRGatewayOverrides `json:"gateway,omitempty"`

	// Env is additional static environment variables to inject into the App.
	// Merged with InjectPREnv values; explicit keys here take precedence.
	// +optional
	Env map[string]string `json:"env,omitempty"`
}

// PRBuildOverrides are the build fields users can set per-GitRepo.
type PRBuildOverrides struct {
	BaseImage      string `json:"baseImage,omitempty"`
	InstallCmd     string `json:"installCmd,omitempty"`
	BuildCmd       string `json:"buildCmd,omitempty"`
	DockerfileMode string `json:"dockerfileMode,omitempty"`
	Dockerfile     string `json:"dockerfile,omitempty"`
	Output         string `json:"output,omitempty"`
	Registry       string `json:"registry,omitempty"`
}

// PRRunOverrides are the run fields users can set per-GitRepo.
type PRRunOverrides struct {
	Command  []string `json:"command,omitempty"`
	Port     int      `json:"port,omitempty"`
	Replicas int      `json:"replicas,omitempty"`
	Registry string   `json:"registry,omitempty"`
}

type PRIngressOverrides struct {
	Enabled     bool              `json:"enabled"`
	ClassName   *string           `json:"className,omitempty"`
	TLSSecret   string            `json:"tlsSecret,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type PRGatewayOverrides struct {
	Enabled     bool              `json:"enabled"`
	GatewayRef  PRGatewayRef      `json:"gatewayRef"`
	TLSSecret   string            `json:"tlsSecret,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type PRGatewayRef struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace,omitempty"`
	SectionName string `json:"sectionName,omitempty"`
}

// ----------------------------------------------------------------
// GitRepo STATUS
// ----------------------------------------------------------------

type GitRepoStatus struct {
	// Phase summarises operator state for this GitRepo.
	Phase string `json:"phase,omitempty"`

	// Message is a human-readable description of the current state.
	Message string `json:"message,omitempty"`

	// WebhookURL is the fully qualified URL the git platform should send
	// events to. Populated when mode=webhook.
	WebhookURL string `json:"webhookUrl,omitempty"`

	// ActivePRDeployments is the count of currently running PR deployments.
	ActivePRDeployments int `json:"activePrDeployments,omitempty"`

	// LastPollTime is the timestamp of the most recent successful poll.
	// Only relevant when mode=poll.
	LastPollTime string `json:"lastPollTime,omitempty"`

	// LastUpdated is the timestamp of the last status change.
	LastUpdated string `json:"lastUpdated,omitempty"`
}

// ----------------------------------------------------------------
// PRDeployment — tracks a single PR's deployment lifecycle
// ----------------------------------------------------------------

type PRDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PRDeploymentSpec   `json:"spec,omitempty"`
	Status PRDeploymentStatus `json:"status,omitempty"`
}

type PRDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PRDeployment `json:"items"`
}

func (in *PRDeployment) DeepCopyObject() runtime.Object {
	out := new(PRDeployment)
	*out = *in
	return out
}

func (in *PRDeploymentList) DeepCopyObject() runtime.Object {
	out := new(PRDeploymentList)
	*out = *in
	return out
}

type PRDeploymentSpec struct {
	// GitRepoRef is the name of the GitRepo that owns this PRDeployment.
	GitRepoRef string `json:"gitRepoRef"`

	// Platform mirrors GitRepo.spec.platform.
	Platform string `json:"platform"`

	// RepoURL is the clone URL of the repository.
	RepoURL string `json:"repoURL"`

	// PRNumber is the pull/merge request number on the platform.
	PRNumber int `json:"prNumber"`

	// Branch is the PR's head branch name.
	Branch string `json:"branch"`

	// HeadSHA is the git commit SHA at the tip of the PR branch.
	HeadSHA string `json:"headSHA"`

	// Author is the username of the PR author.
	Author string `json:"author"`

	// AuthorAssociation is the platform-reported relationship of the author
	// to the repository (e.g. MEMBER, COLLABORATOR).
	AuthorAssociation string `json:"authorAssociation"`

	// Title is the PR title (informational).
	Title string `json:"title"`

	// AppRef is the name of the kube-deploy App CR we created.
	AppRef string `json:"appRef"`

	// AppNamespace is the namespace of the kube-deploy App CR.
	AppNamespace string `json:"appNamespace"`
}

type PRDeploymentStatus struct {
	// State is the high-level lifecycle state.
	// +kubebuilder:validation:Enum=pending;deploying;running;error;closed;deleting
	State string `json:"state,omitempty"`

	// AppPhase mirrors the kube-deploy App's status.phase.
	AppPhase string `json:"appPhase,omitempty"`

	// URL is the live preview URL for this PR (ingress host).
	URL string `json:"url,omitempty"`

	// Message is a human-readable status description or error.
	Message string `json:"message,omitempty"`

	// LastUpdated is the timestamp of the last status change.
	LastUpdated string `json:"lastUpdated,omitempty"`
}
