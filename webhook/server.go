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

	"kube-gitops/api"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Handler is a function that processes a validated webhook payload.
// The platform adapter parses the raw body into a PREvent and calls back.
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

// RouteEntry maps a webhook path to its GitRepo and handler.
type RouteEntry struct {
	GitRepo api.GitRepo
	Handler Handler
}

// Server is the webhook HTTP server. It multiplexes incoming requests
// across all registered GitRepo routes, validates HMAC signatures,
// and dispatches normalized PREvents to registered handlers.
type Server struct {
	client  client.Client
	mux     *http.ServeMux
	server  *http.Server
	mu      sync.RWMutex
	routes  map[string]RouteEntry // path → entry
	handler Handler
}

func NewServer(c client.Client, addr string, h Handler) *Server {
	s := &Server{
		client:  c,
		routes:  make(map[string]RouteEntry),
		handler: h,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/", s.dispatch)
	s.mux = mux
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
	s.routes[path] = RouteEntry{GitRepo: gr}
	s.mu.Unlock()
}

// Deregister removes the route for a GitRepo.
func (s *Server) Deregister(gr api.GitRepo) {
	s.mu.Lock()
	delete(s.routes, webhookPath(gr))
	s.mu.Unlock()
}

// Start begins serving. Blocks until ctx is cancelled.
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

	path := r.URL.Path

	s.mu.RLock()
	entry, ok := s.routes[path]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	gr := entry.GitRepo

	// Read body — cap at 10MB to prevent abuse
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

		platform := gr.Spec.Platform
		if !validateSignature(platform, r, body, secret) {
			logger.Info("webhook signature validation failed",
				"gitrepo", gr.Name, "path", path, "platform", platform)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	// Dispatch to platform adapter for parsing
	event, skip, err := parsePlatformEvent(gr.Spec.Platform, r, body)
	if err != nil {
		logger.Error(err, "failed to parse webhook event", "gitrepo", gr.Name)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if skip {
		// Not a PR-related event — acknowledge and move on
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

// loadSecret reads the named k8s Secret and returns the value of the "secret" key.
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

// validateSignature checks the platform-appropriate HMAC signature header.
// GitHub and Gitea use X-Hub-Signature-256 with HMAC-SHA256.
// GitLab uses X-Gitlab-Token as a plain token comparison.
func validateSignature(platform string, r *http.Request, body, secret []byte) bool {
	switch platform {
	case "github", "gitea", "forgejo":
		sig := r.Header.Get("X-Hub-Signature-256")
		if sig == "" {
			return false
		}
		sig = strings.TrimPrefix(sig, "sha256=")
		mac := hmac.New(sha256.New, secret)
		mac.Write(body)
		expected := hex.EncodeToString(mac.Sum(nil))
		return hmac.Equal([]byte(sig), []byte(expected))

	case "gitlab":
		// GitLab sends the token as a plain header value
		token := r.Header.Get("X-Gitlab-Token")
		return hmac.Equal([]byte(token), secret)

	default:
		return false
	}
}

// webhookPath returns the HTTP path for a GitRepo's webhook endpoint.
// Uses spec.trigger.webhookPath if set, otherwise defaults to
// /webhook/<namespace>/<name>
func webhookPath(gr api.GitRepo) string {
	if gr.Spec.Trigger.WebhookPath != "" {
		p := gr.Spec.Trigger.WebhookPath
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		return p
	}
	return fmt.Sprintf("/webhook/%s/%s", gr.Namespace, gr.Name)
}
