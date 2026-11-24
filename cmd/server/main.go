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

	"github.com/JoshTeperman/interview-project-agent/internal/auth"
	"github.com/JoshTeperman/interview-project-agent/internal/config"
	"github.com/JoshTeperman/interview-project-agent/internal/mail"
	"github.com/JoshTeperman/interview-project-agent/internal/server"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("invalid configuration; refusing to start", "error", err.Error())
		os.Exit(2)
	}
	log.Info("config loaded", "config", cfg.String())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sender := &mail.SMTP{Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser, Pass: cfg.SMTPPass, FromName: cfg.SMTPFromName}
	authSvc := auth.New(cfg, sender, log, nil)
	go authSvc.RunPruner(ctx, time.Minute)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           server.New(cfg, authSvc, log).Handler(),
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
