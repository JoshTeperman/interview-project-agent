// Command server runs the interview-project-agent web app.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JoshTeperman/interview-project-agent/internal/agent"
	"github.com/JoshTeperman/interview-project-agent/internal/auth"
	"github.com/JoshTeperman/interview-project-agent/internal/chat"
	"github.com/JoshTeperman/interview-project-agent/internal/config"
	"github.com/JoshTeperman/interview-project-agent/internal/llm"
	"github.com/JoshTeperman/interview-project-agent/internal/mail"
	"github.com/JoshTeperman/interview-project-agent/internal/questionlog"
	"github.com/JoshTeperman/interview-project-agent/internal/server"
	"github.com/JoshTeperman/interview-project-agent/internal/snapshot"
	"github.com/JoshTeperman/interview-project-agent/prompts"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("invalid configuration; refusing to start", "error", err.Error())
		os.Exit(2)
	}
	log.Info("config loaded", "config", cfg.String())

	// Everything chat needs is checked before serving, so a bad deploy fails at startup.
	snap, err := snapshot.Open(cfg.SnapshotDir, snapshot.Limits{MaxToolChars: cfg.ToolOutputMaxChars, MaxOverviewChars: cfg.OverviewMaxChars})
	if err != nil {
		fatal(log, "cannot open snapshot", err)
	}
	provider, err := llm.New(llm.Options{
		Provider: cfg.LLMProvider, Model: cfg.LLMModel, APIKey: cfg.APIKey(),
		MaxOutputTokens: int64(cfg.LLMMaxOutputTokens), Timeout: cfg.LLMTimeout,
	})
	if err != nil {
		fatal(log, "cannot set up the LLM provider", err)
	}
	qlog, err := questionlog.Open(cfg.DataDir, cfg.OpenAIAPIKey, cfg.AnthropicAPIKey, cfg.SMTPPass)
	if err != nil {
		fatal(log, "cannot open the question log", err)
	}
	defer qlog.Close()
	ag := agent.New(snap, cfg.ToolStepCap)
	log.Info("snapshot loaded", "project", snap.Project.Name, "system_prompt_chars", len(ag.System()),
		"prompt_version", prompts.Version(), "starter_questions", len(snap.Project.StarterQuestions))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sender := &mail.SMTP{Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser, Pass: cfg.SMTPPass, FromName: cfg.SMTPFromName}
	authSvc := auth.New(cfg, sender, log, nil)
	go authSvc.RunPruner(ctx, time.Minute)
	chatSvc := chat.New(ag, provider, qlog, log, chat.Options{
		PerSessionMinute: cfg.ChatPerSessionMinute, PerIPMinute: cfg.ChatPerIPMinute,
		MaxMessageChars: cfg.MaxMessageChars, IdleTTL: cfg.SessionTTL,
		QuestionTimeout: cfg.QuestionTimeout, ContextMaxInputTokens: cfg.ContextMaxInputTokens,
		TurnTokenBudget: chat.TurnTokenBudget(cfg.ToolStepCap, cfg.ToolOutputMaxChars, cfg.MaxMessageChars),
	}, nil)
	go chatSvc.RunPruner(ctx, time.Minute)
	app := server.New(cfg, authSvc, log)
	app.EnableChat(chatSvc, snap.Project.StarterQuestions)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
		authSvc.Wait() // let in-flight code emails finish
	}()

	log.Info("listening", "addr", cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server failed", "error", err.Error())
		os.Exit(1)
	}
}

func fatal(log *slog.Logger, msg string, err error) {
	log.Error(msg+"; refusing to start", "error", err.Error())
	os.Exit(2)
}
