package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type Server struct {
	config      *Config
	provisioner *Provisioner
	tracker     *Tracker
	logger      *slog.Logger
	httpServer  *http.Server
	startTime   time.Time
}

func NewServer(cfg *Config, provisioner *Provisioner, tracker *Tracker, logger *slog.Logger) *Server {
	s := &Server{
		config:      cfg,
		provisioner: provisioner,
		tracker:     tracker,
		logger:      logger,
		startTime:   time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", s.handleWebhook)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /status", s.handleStatus)

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	return s
}

func (s *Server) Start() error {
	s.logger.Info("starting server", "port", s.config.Port)
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.logger.Error("read webhook body", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Validate signature
	sig := r.Header.Get("X-Hub-Signature-256")
	if !validateSignature(body, sig, []byte(s.config.WebhookSecret)) {
		s.logger.Warn("invalid webhook signature")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Only handle workflow_job events
	eventType := r.Header.Get("X-GitHub-Event")
	if eventType != "workflow_job" {
		s.logger.Debug("ignoring event", "type", eventType)
		w.WriteHeader(http.StatusOK)
		return
	}

	event, err := parseWorkflowJobEvent(body)
	if err != nil {
		s.logger.Error("parse webhook", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	log := s.logger.With("action", event.Action, "job_id", event.WorkflowJob.ID,
		"labels", event.WorkflowJob.Labels)

	switch event.Action {
	case "queued":
		// Only provision for jobs with "exe" label but not "exe-builder"
		if !hasLabel(event.WorkflowJob.Labels, "exe") {
			log.Debug("no exe label, ignoring")
			w.WriteHeader(http.StatusOK)
			return
		}
		if hasLabel(event.WorkflowJob.Labels, "exe-builder") {
			log.Debug("exe-builder label, ignoring")
			w.WriteHeader(http.StatusOK)
			return
		}

		log.Info("job queued, provisioning")
		w.WriteHeader(http.StatusOK)
		go s.provisioner.Provision(context.Background(), *event)

	case "in_progress":
		if record, ok := s.tracker.Get(event.WorkflowJob.ID); ok {
			s.tracker.Update(event.WorkflowJob.ID, StatusRunning)
			log.Info("job in progress", "vm", record.VMName)
		}
		w.WriteHeader(http.StatusOK)

	case "completed":
		log.Info("job completed, destroying")
		w.WriteHeader(http.StatusOK)
		go s.provisioner.Destroy(context.Background(), *event)

	default:
		log.Debug("unhandled action")
		w.WriteHeader(http.StatusOK)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	resp := map[string]any{
		"status":     "ok",
		"uptime":     time.Since(s.startTime).String(),
		"active_vms": s.tracker.Count(),
		"max_vms":    s.config.MaxVMs,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	resp := map[string]any{
		"active_vms": s.tracker.ActiveVMs(),
		"count":      s.tracker.Count(),
		"max_vms":    s.config.MaxVMs,
		"uptime":     time.Since(s.startTime).String(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
