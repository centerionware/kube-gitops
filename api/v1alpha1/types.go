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
	// +kubebuilder:validation:Enum=github;gitlab;gitea;forgejo
	Platform string `json:"platform"`

	// Repo is the full HTTPS URL of the repository.
	Repo string `json:"repo"`

	// GitSecret is the name of a Kubernetes Secret containing platform credentials.
	//   username + password (token) — for HTTPS clone and API calls
	//   ssh-privatekey             — for SSH clone only (poll mode requires token)
	GitSecret string `json:"gitSecret"`

	// Trigger defines how the operator detects new pull requests.
	Trigger TriggerSpec `json:"trigger"`

	// PRPolicy defines the trust model — who may trigger a deployment.
	PRPolicy PRPolicySpec `json:"prPolicy,omitempty"`

	// PRDeploy defines how to build and deploy each PR preview.
	PRDeploy PRDeploySpec `json:"prDeploy,omitempty"`

	// Notify configures automated comments posted back to the PR thread.
	Notify NotifySpec `json:"notify,omitempty"`
}

// ----------------------------------------------------------------
// TRIGGER
// ----------------------------------------------------------------

type TriggerSpec struct {
	// Mode: webhook (preferred, interrupt-driven) or poll (periodic).
	// +kubebuilder:validation:Enum=webhook;poll
	Mode string `json:"mode"`

	// PollInterval is how often to query the platform API in poll mode.
	// Go duration string e.g. "2m". Default: "2m".
	// +optional
	PollInterval string `json:"pollInterval,omitempty"`

	// WebhookSecret is the name of a Secret with a "secret" key used for
	// HMAC-SHA256 payload validation. Required when mode=webhook.
	// +optional
	WebhookSecret string `json:"webhookSecret,omitempty"`

	// WebhookPath overrides the default /webhook/<namespace>/<name> path.
	// +optional
	WebhookPath string `json:"webhookPath,omitempty"`
}

// ----------------------------------------------------------------
// PR POLICY
// ----------------------------------------------------------------

type PRPolicySpec struct {
	// AllowedAuthorAssociations are the platform author roles that auto-trigger.
	// GitHub: OWNER, MEMBER, COLLABORATOR, CONTRIBUTOR, FIRST_TIME_CONTRIBUTOR, NONE
	// Defaults to ["OWNER", "MEMBER", "COLLABORATOR"].
	// +optional
	AllowedAuthorAssociations []string `json:"allowedAuthorAssociations,omitempty"`

	// RequireLabel gates deployment on the PR carrying this label.
	// +optional
	RequireLabel string `json:"requireLabel,omitempty"`

	// AllowCommentTrigger enables /deploy-style comment triggers.
	// +optional
	AllowCommentTrigger bool `json:"allowCommentTrigger,omitempty"`

	// CommentTriggerPhrase is the phrase that triggers a deploy. Default: "/deploy".
	// +optional
	CommentTriggerPhrase string `json:"commentTriggerPhrase,omitempty"`

	// AllowedCommenters is an explicit allowlist of usernames for comment triggers.
	// Use ["*"] to allow any member.
	// +optional
	AllowedCommenters []string `json:"allowedCommenters,omitempty"`
}

// ----------------------------------------------------------------
// NOTIFY — automated PR comments
// ----------------------------------------------------------------

type NotifySpec struct {
	// OnDeploy posts a comment when the preview deployment becomes ready.
	// Default: true.
	// +optional
	OnDeploy *bool `json:"onDeploy,omitempty"`

	// OnError posts a comment when the deployment fails.
	// Default: true.
	// +optional
	OnError *bool `json:"onError,omitempty"`

	// OnClose posts a comment when the preview is torn down.
	// Default: false.
	// +optional
	OnClose *bool `json:"onClose,omitempty"`

	// DeployTemplate is the Go template for the deploy-ready comment.
	// Available vars: .URL .PRNumber .Branch .SHA .Author .RepoName
	// Default: "🚀 Preview deployed: {{.URL}}"
	// +optional
	DeployTemplate string `json:"deployTemplate,omitempty"`

	// ErrorTemplate is the Go template for the error comment.
	// Available vars: .Error .PRNumber .Branch .SHA .Author .RepoName
	// Default: "❌ Preview deployment failed: {{.Error}}"
	// +optional
	ErrorTemplate string `json:"errorTemplate,omitempty"`

	// CloseTemplate is the Go template for the teardown comment.
	// Default: "🧹 Preview environment removed."
	// +optional
	CloseTemplate string `json:"closeTemplate,omitempty"`
}

// ----------------------------------------------------------------
// PR DEPLOY
// ----------------------------------------------------------------

type PRDeploySpec struct {
	// Namespace to create kube-deploy App CRs in.
	// Defaults to the GitRepo's own namespace. Created if it doesn't exist.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// NameTemplate is a Go template for the generated App CR name.
	// Vars: .RepoName .PRNumber .BranchSlug
	// Default: "{{.RepoName}}-pr-{{.PRNumber}}"
	// +optional
	NameTemplate string `json:"nameTemplate,omitempty"`

	// IngressHostTemplate is a Go template for the preview ingress hostname.
	// Vars: .RepoName .PRNumber .BranchSlug .BaseDomain
	// Default: "pr-{{.PRNumber}}.{{.RepoName}}.{{.BaseDomain}}"
	// +optional
	IngressHostTemplate string `json:"ingressHostTemplate,omitempty"`

	// BaseDomain is substituted as .BaseDomain in IngressHostTemplate.
	// +optional
	BaseDomain string `json:"baseDomain,omitempty"`

	// InjectPREnv injects PR metadata as env vars into the App. Default: true.
	// Vars injected: GITOPS_PR_NUMBER, GITOPS_PR_BRANCH, GITOPS_PR_SHA,
	//                GITOPS_PR_AUTHOR, GITOPS_PR_TITLE, GITOPS_REPO_URL,
	//                GITOPS_PREVIEW_URL
	// +optional
	InjectPREnv *bool `json:"injectPREnv,omitempty"`

	// Build overrides for the generated App's spec.build.
	// gitSecret is always forwarded from GitRepo.spec.gitSecret.
	// +optional
	Build PRBuildOverrides `json:"build,omitempty"`

	// Run overrides for the generated App's spec.run.
	// +optional
	Run PRRunOverrides `json:"run,omitempty"`

	// Ingress configures the preview ingress. Mutually exclusive with Gateway.
	// +optional
	Ingress *PRIngressOverrides `json:"ingress,omitempty"`

	// Gateway configures the preview HTTPRoute. Mutually exclusive with Ingress.
	// +optional
	Gateway *PRGatewayOverrides `json:"gateway,omitempty"`

	// Env is static environment variables merged into the App (win over InjectPREnv).
	// +optional
	Env map[string]string `json:"env,omitempty"`
}

type PRBuildOverrides struct {
	BaseImage      string `json:"baseImage,omitempty"`
	InstallCmd     string `json:"installCmd,omitempty"`
	BuildCmd       string `json:"buildCmd,omitempty"`
	DockerfileMode string `json:"dockerfileMode,omitempty"`
	Dockerfile     string `json:"dockerfile,omitempty"`
	Output         string `json:"output,omitempty"`
	Registry       string `json:"registry,omitempty"`
}

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
	// Phase: Ready, Error, Registering, Superseded
	Phase string `json:"phase,omitempty"`

	// Message is a human-readable status description.
	Message string `json:"message,omitempty"`

	// WebhookURL is the fully qualified public URL for this repo's webhook endpoint.
	WebhookURL string `json:"webhookUrl,omitempty"`

	// WebhookStatus describes the current state of platform webhook registration.
	// One of: registered, unregistered, failed, manual
	WebhookStatus string `json:"webhookStatus,omitempty"`

	// WebhookID is the platform-assigned hook ID, present when WebhookStatus=registered.
	// This is what gets used to deregister on GitRepo deletion.
	WebhookID string `json:"webhookId,omitempty"`

	// ActivePRDeployments is the count of currently live PR previews.
	ActivePRDeployments int `json:"activePrDeployments,omitempty"`

	// LastPollTime is the timestamp of the last successful API poll.
	LastPollTime string `json:"lastPollTime,omitempty"`

	// LastUpdated is the timestamp of the last status change.
	LastUpdated string `json:"lastUpdated,omitempty"`
}

// ----------------------------------------------------------------
// PRDeployment — tracks one PR's full deployment lifecycle
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
	// GitRepoRef is the parent GitRepo name.
	GitRepoRef string `json:"gitRepoRef"`

	Platform  string `json:"platform"`
	RepoURL   string `json:"repoURL"`
	// CloneURL is the HEAD repo clone URL — differs from RepoURL for fork PRs.
	CloneURL  string `json:"cloneURL,omitempty"`
	PRNumber  int    `json:"prNumber"`
	Branch    string `json:"branch"`
	HeadSHA   string `json:"headSHA"`
	Author    string `json:"author"`
	AuthorAssociation string `json:"authorAssociation"`
	Title     string `json:"title"`

	// AppRef is the name of the kube-deploy App CR we own.
	AppRef       string `json:"appRef"`
	AppNamespace string `json:"appNamespace"`
}

type PRDeploymentStatus struct {
	// State: pending, deploying, running, error, closed, deleting
	State string `json:"state,omitempty"`

	// AppPhase mirrors kube-deploy App status.phase.
	AppPhase string `json:"appPhase,omitempty"`

	// Image mirrors kube-deploy App status.image.
	Image string `json:"image,omitempty"`

	// Commit mirrors kube-deploy App status.commit.
	Commit string `json:"commit,omitempty"`

	// URL is the live preview URL.
	URL string `json:"url,omitempty"`

	// Message is a human-readable status or error description.
	Message string `json:"message,omitempty"`

	// NotifiedDeploy tracks whether we've posted the deploy-ready comment.
	NotifiedDeploy bool `json:"notifiedDeploy,omitempty"`

	// NotifiedError tracks whether we've posted the error comment.
	NotifiedError bool `json:"notifiedError,omitempty"`

	// LastUpdated is the timestamp of the last status change.
	LastUpdated string `json:"lastUpdated,omitempty"`
}
