package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	api "kube-gitops/api/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Handler processes a validated, parsed webhook event.
type Handler func(ctx context.Context, event PREvent) error

// PREvent is the normalized representation of a pull request event
// across all supported platforms.
type PREvent struct {
	// Action is one of: opened, synchronize, closed, labeled, unlabeled, comment
	Action string

	// Platform is the source platform: github, gitlab, gitea, forgejo
	Platform string

	// RepoURL is the HTTPS clone URL of the repository
	RepoURL string

	// PRNumber is the pull/merge request number
	PRNumber int

	// Branch is the PR head branch name
	Branch string

	// HeadSHA is the commit SHA at the tip of the PR branch
	HeadSHA string

	// Author is the username of the PR author
	Author string

	// AuthorAssociation is the platform-reported role of the author
	// e.g. OWNER, MEMBER, COLLABORATOR, CONTRIBUTOR, NONE
	AuthorAssociation string

	// Title is the PR title
	Title string

	// Labels are the current labels on the PR
	Labels []string

	// CommentBody is populated when Action == "comment"
	CommentBody string

	// CommentAuthor is the commenter's username, when Action == "comment"
	CommentAuthor string

	// CommentAuthorAssociation is the commenter's association, when Action == "comment"
	CommentAuthorAssociation string
}

// routeEntry maps a webhook path to its GitRepo.
type routeEntry struct {
	gitRepo api.GitRepo
}

// Server is the webhook HTTP server. It multiplexes incoming requests
// across all registered GitRepo routes, validates HMAC signatures,
// and dispatches normalized PREvents to the registered handler.
type Server struct {
	client  client.Client
	server  *http.Server
	mu      sync.RWMutex
	routes  map[string]routeEntry // path → gitrepo
	handler Handler
}

func NewServer(c client.Client, addr string, h Handler) *Server {
	s := &Server{
		client:  c,
		routes:  make(map[string]routeEntry),
		handler: h,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/", s.dispatch)
	s.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	return s
}

// Register adds or updates the route for a GitRepo.
func (s *Server) Register(gr api.GitRepo) {
	path := webhookPath(gr)
	s.mu.Lock()
	s.routes[path] = routeEntry{gitRepo: gr}
	s.mu.Unlock()
}

// Deregister removes the route for a GitRepo.
func (s *Server) Deregister(gr api.GitRepo) {
	s.mu.Lock()
	delete(s.routes, webhookPath(gr))
	s.mu.Unlock()
}

// Start begins serving. Satisfies controller-runtime's manager.Runnable.
// Blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	logger := log.FromContext(ctx)
	logger.Info("webhook server listening", "addr", s.server.Addr)

	errCh := make(chan error, 1)
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.server.Shutdown(shutCtx)
	}
}

func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := log.FromContext(ctx)

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	entry, ok := s.routes[r.URL.Path]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	gr := entry.gitRepo

	// Cap body at 10MB to prevent abuse
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Validate HMAC signature if a webhookSecret is configured
	if gr.Spec.Trigger.WebhookSecret != "" {
		secret, err := s.loadSecret(ctx, gr.Namespace, gr.Spec.Trigger.WebhookSecret)
		if err != nil {
			logger.Error(err, "failed to load webhook secret",
				"gitrepo", gr.Name, "secret", gr.Spec.Trigger.WebhookSecret)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !validateSignature(gr.Spec.Platform, r, body, secret) {
			logger.Info("webhook signature validation failed",
				"gitrepo", gr.Name, "path", r.URL.Path, "platform", gr.Spec.Platform)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	event, skip, err := parsePlatformEvent(gr.Spec.Platform, r, body)
	if err != nil {
		logger.Error(err, "failed to parse webhook event", "gitrepo", gr.Name)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if skip {
		w.WriteHeader(http.StatusOK)
		return
	}

	event.Platform = gr.Spec.Platform
	event.RepoURL = gr.Spec.Repo

	if err := s.handler(ctx, event); err != nil {
		logger.Error(err, "handler error", "gitrepo", gr.Name, "pr", event.PRNumber)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) loadSecret(ctx context.Context, namespace, name string) ([]byte, error) {
	var secret corev1.Secret
	if err := s.client.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}, &secret); err != nil {
		return nil, fmt.Errorf("get secret %s/%s: %w", namespace, name, err)
	}
	val, ok := secret.Data["secret"]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s has no 'secret' key", namespace, name)
	}
	return val, nil
}

// validateSignature checks the platform-appropriate HMAC header.
// GitHub, Gitea, Forgejo: X-Hub-Signature-256 (HMAC-SHA256, hex-encoded).
// GitLab: X-Gitlab-Token (plain token comparison).
func validateSignature(platform string, r *http.Request, body, secret []byte) bool {
	switch platform {
	case "github", "gitea", "forgejo":
		sig := strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=")
		if sig == "" {
			return false
		}
		mac := hmac.New(sha256.New, secret)
		mac.Write(body)
		return hmac.Equal([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil))))

	case "gitlab":
		return hmac.Equal([]byte(r.Header.Get("X-Gitlab-Token")), secret)

	default:
		return false
	}
}

// webhookPath returns the HTTP path for a GitRepo's webhook endpoint.
func webhookPath(gr api.GitRepo) string {
	if p := gr.Spec.Trigger.WebhookPath; p != "" {
		if !strings.HasPrefix(p, "/") {
			return "/" + p
		}
		return p
	}
	return fmt.Sprintf("/webhook/%s/%s", gr.Namespace, gr.Name)
}
