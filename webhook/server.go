package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"kube-gitops/api/v1alpha1"

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
	Action                   string
	Platform                 string
	RepoURL                  string
	PRNumber                 int
	Branch                   string
	HeadSHA                  string
	Author                   string
	AuthorAssociation        string
	Title                    string
	Labels                   []string
	CommentBody              string
	CommentAuthor            string
	CommentAuthorAssociation string
}

type routeEntry struct {
	gitRepo v1alpha1.GitRepo
}

// Server is a single HTTP server handling all traffic on one port:
//
//	GET  /healthz        — liveness probe
//	GET  /readyz         — readiness probe
//	GET  /metrics        — basic runtime metrics (JSON)
//	POST /webhook/...    — per-GitRepo webhook endpoints
type Server struct {
	client  client.Client
	server  *http.Server
	mu      sync.RWMutex
	routes  map[string]routeEntry
	handler Handler
	ready   bool
}

func NewServer(c client.Client, addr string, h Handler) *Server {
	s := &Server{
		client:  c,
		routes:  make(map[string]routeEntry),
		handler: h,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/webhook/", s.handleWebhook)

	s.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	return s
}

// Register adds or updates the route for a GitRepo.
func (s *Server) Register(gr v1alpha1.GitRepo) {
	s.mu.Lock()
	s.routes[webhookPath(gr)] = routeEntry{gitRepo: gr}
	s.mu.Unlock()
}

// Deregister removes the route for a GitRepo.
func (s *Server) Deregister(gr v1alpha1.GitRepo) {
	s.mu.Lock()
	delete(s.routes, webhookPath(gr))
	s.mu.Unlock()
}

// Start satisfies controller-runtime manager.Runnable.
func (s *Server) Start(ctx context.Context) error {
	logger := log.FromContext(ctx)
	logger.Info("HTTP server listening", "addr", s.server.Addr)

	s.ready = true

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
		s.ready = false
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.server.Shutdown(shutCtx)
	}
}

// ── Route handlers ────────────────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.ready {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ready"}`))
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"status":"not ready"}`))
	}
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	routeCount := len(s.routes)
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"registered_webhooks": routeCount,
		"uptime":              time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
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

	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	if gr.Spec.Trigger.WebhookSecret != "" {
		secret, err := s.loadSecret(ctx, gr.Namespace, gr.Spec.Trigger.WebhookSecret)
		if err != nil {
			logger.Error(err, "failed to load webhook secret", "gitrepo", gr.Name)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !validateSignature(gr.Spec.Platform, r, body, secret) {
			logger.Info("webhook signature validation failed", "gitrepo", gr.Name)
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

// ── Helpers ───────────────────────────────────────────────────────────────────

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

func webhookPath(gr v1alpha1.GitRepo) string {
	if p := gr.Spec.Trigger.WebhookPath; p != "" {
		if !strings.HasPrefix(p, "/") {
			return "/" + p
		}
		return p
	}
	return fmt.Sprintf("/webhook/%s/%s", gr.Namespace, gr.Name)
}

// PublicPath returns the webhook HTTP path for a GitRepo.
// Exported so controllers can build the full public URL.
func PublicPath(gr v1alpha1.GitRepo) string {
	return webhookPath(gr)
}
