package builder

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	api "kube-gitops/api/v1alpha1"
	"kube-gitops/kubedeploy"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TemplateVars are the variables available in nameTemplate and ingressHostTemplate.
type TemplateVars struct {
	RepoName   string // last path segment of repo URL, lowercased
	PRNumber   int
	BranchSlug string // branch name sanitized for use in DNS/k8s names
	BaseDomain string
}

// BuildApp constructs the kube-deploy App CR that will be created for a PR.
// It reads the PRDeployment for PR-specific data and the GitRepo for config.
func BuildApp(gr api.GitRepo, pr api.PRDeployment) (*kubedeploy.App, error) {
	cfg := gr.Spec.PRDeploy

	vars := TemplateVars{
		RepoName:   repoName(gr.Spec.Repo),
		PRNumber:   pr.Spec.PRNumber,
		BranchSlug: slugify(pr.Spec.Branch),
		BaseDomain: cfg.BaseDomain,
	}

	// App name
	nameTpl := cfg.NameTemplate
	if nameTpl == "" {
		nameTpl = "{{.RepoName}}-pr-{{.PRNumber}}"
	}
	name, err := renderTemplate("name", nameTpl, vars)
	if err != nil {
		return nil, fmt.Errorf("render name template: %w", err)
	}
	// Ensure name is a valid k8s name
	name = slugify(name)

	// App CR always lives in the GitRepo's namespace — that's where the git
	// secret is, and kube-deploy looks for secrets in the App CR's namespace.
	// prDeploy.namespace controls where kube-deploy puts the Deployment/Service/
	// Ingress it creates, not where the App CR itself lives.
	namespace := gr.Namespace

	// Build env map — PR metadata first, then static overrides
	env := make(map[string]string)

	// Resolve the correct repo URL for this PR.
	// For fork PRs CloneURL is the fork — that's what we build from.
	// For same-repo PRs CloneURL == RepoURL.
	buildRepo := pr.Spec.CloneURL
	if buildRepo == "" {
		buildRepo = pr.Spec.RepoURL
	}

	injectPR := cfg.InjectPREnv == nil || *cfg.InjectPREnv // default true
	if injectPR {
		env["GITOPS_PR_NUMBER"] = strconv.Itoa(pr.Spec.PRNumber)
		env["GITOPS_PR_BRANCH"] = pr.Spec.Branch
		env["GITOPS_PR_SHA"] = pr.Spec.HeadSHA
		env["GITOPS_PR_AUTHOR"] = pr.Spec.Author
		env["GITOPS_REPO_URL"] = buildRepo       // fork URL for fork PRs
		env["GITOPS_UPSTREAM_URL"] = pr.Spec.RepoURL // always the upstream
	}

	// Static env from GitRepo spec — these win over injected values
	for k, v := range cfg.Env {
		env[k] = v
	}
	if pr.Spec.Branch == "" {
		return nil, fmt.Errorf("PRDeployment %s has empty Branch — cannot build correct commit", pr.Name)
	}
	build := kubedeploy.BuildSpec{
		Branch:    pr.Spec.Branch,
		GitSecret: gr.Spec.GitSecret,
	}
	if cfg.Build.BaseImage != "" {
		build.BaseImage = cfg.Build.BaseImage
	}
	if cfg.Build.InstallCmd != "" {
		build.InstallCmd = cfg.Build.InstallCmd
	}
	if cfg.Build.BuildCmd != "" {
		build.BuildCmd = cfg.Build.BuildCmd
	}
	if cfg.Build.DockerfileMode != "" {
		build.DockerfileMode = cfg.Build.DockerfileMode
	}
	if cfg.Build.Dockerfile != "" {
		build.Dockerfile = cfg.Build.Dockerfile
	}
	if cfg.Build.Output != "" {
		build.Output = cfg.Build.Output
	}
	if cfg.Build.Registry != "" {
		build.Registry = cfg.Build.Registry
	}

	// Run spec
	run := kubedeploy.RunSpec{}
	if len(cfg.Run.Command) > 0 {
		run.Command = cfg.Run.Command
	}
	if cfg.Run.Port != 0 {
		run.Port = cfg.Run.Port
	}
	run.Replicas = 1
	if cfg.Run.Replicas > 0 {
		run.Replicas = cfg.Run.Replicas
	}
	if cfg.Run.Registry != "" {
		run.Registry = cfg.Run.Registry
	}

	// Ingress / Gateway
	var ingress *kubedeploy.IngressSpec
	var gateway *kubedeploy.GatewaySpec

	if cfg.Gateway != nil && cfg.Gateway.Enabled {
		gateway = &kubedeploy.GatewaySpec{
			Enabled: true,
			GatewayRef: kubedeploy.GatewayRefSpec{
				Name:        cfg.Gateway.GatewayRef.Name,
				Namespace:   cfg.Gateway.GatewayRef.Namespace,
				SectionName: cfg.Gateway.GatewayRef.SectionName,
			},
			TLSSecret:   cfg.Gateway.TLSSecret,
			Annotations: cfg.Gateway.Annotations,
		}
		host, err := renderIngressHost(cfg, vars)
		if err != nil {
			return nil, err
		}
		gateway.Hostnames = []string{host}
	} else {
		// Default to ingress
		host, err := renderIngressHost(cfg, vars)
		if err != nil {
			return nil, err
		}
		ing := &kubedeploy.IngressSpec{
			Enabled: true,
			Host:    host,
		}
		if cfg.Ingress != nil {
			ing.ClassName = cfg.Ingress.ClassName
			ing.TLSSecret = cfg.Ingress.TLSSecret
			ing.Annotations = cfg.Ingress.Annotations
			if !cfg.Ingress.Enabled {
				ing.Enabled = false
			}
		}
		ingress = ing
	}

	app := &kubedeploy.App{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "kube-deploy.centerionware.app/v1alpha1",
			Kind:       "App",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"kube-gitops.centerionware.app/gitrepo":   gr.Name,
				"kube-gitops.centerionware.app/pr-number": strconv.Itoa(pr.Spec.PRNumber),
				"kube-gitops.centerionware.app/platform":  gr.Spec.Platform,
			},
			Annotations: map[string]string{
				"kube-gitops.centerionware.app/pr-branch": pr.Spec.Branch,
				"kube-gitops.centerionware.app/pr-sha":    pr.Spec.HeadSHA,
				"kube-gitops.centerionware.app/pr-author": pr.Spec.Author,
			},
		},
		Spec: kubedeploy.AppSpec{
			Repo:    buildRepo, // fork URL for fork PRs, upstream URL for same-repo PRs
			Env:     env,
			Build:   build,
			Run:     run,
			Ingress: ingress,
			Gateway: gateway,
		},
	}

	return app, nil
}

// AppName returns the computed App CR name for a PR, without building the
// full object. Useful for lookups.
func AppName(gr api.GitRepo, prNumber int, branch string) (string, error) {
	cfg := gr.Spec.PRDeploy
	vars := TemplateVars{
		RepoName:   repoName(gr.Spec.Repo),
		PRNumber:   prNumber,
		BranchSlug: slugify(branch),
		BaseDomain: cfg.BaseDomain,
	}
	nameTpl := cfg.NameTemplate
	if nameTpl == "" {
		nameTpl = "{{.RepoName}}-pr-{{.PRNumber}}"
	}
	name, err := renderTemplate("name", nameTpl, vars)
	if err != nil {
		return "", err
	}
	return slugify(name), nil
}

// AppNamespace returns the namespace the App CR is created in.
// This is always the GitRepo's own namespace so kube-deploy can find the git secret.
func AppNamespace(gr api.GitRepo) string {
	return gr.Namespace
}

func renderIngressHost(cfg api.PRDeploySpec, vars TemplateVars) (string, error) {
	tpl := cfg.IngressHostTemplate
	if tpl == "" {
		tpl = "pr-{{.PRNumber}}.{{.RepoName}}.{{.BaseDomain}}"
	}
	return renderTemplate("host", tpl, vars)
}

func renderTemplate(name, tpl string, vars TemplateVars) (string, error) {
	t, err := template.New(name).Parse(tpl)
	if err != nil {
		return "", fmt.Errorf("parse template %q: %w", tpl, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("execute template %q: %w", tpl, err)
	}
	return buf.String(), nil
}

// repoName extracts the repository name from a URL.
// https://github.com/org/myapp → myapp
func repoName(repoURL string) string {
	parts := strings.Split(strings.TrimSuffix(repoURL, ".git"), "/")
	if len(parts) == 0 {
		return "repo"
	}
	return strings.ToLower(parts[len(parts)-1])
}

var nonAlphanumDash = regexp.MustCompile(`[^a-z0-9-]+`)
var multipleDashes = regexp.MustCompile(`-{2,}`)

// slugify converts a string into a valid lowercase k8s/DNS name segment.
func slugify(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "_", "-")
	s = nonAlphanumDash.ReplaceAllString(s, "-")
	s = multipleDashes.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	// k8s names max 253 chars
	if len(s) > 253 {
		s = s[:253]
	}
	return s
}
