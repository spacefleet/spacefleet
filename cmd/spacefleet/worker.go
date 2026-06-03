package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spacefleet/spacefleet/lib/clusters"
	"github.com/spacefleet/spacefleet/lib/config"
	"github.com/spacefleet/spacefleet/lib/db"
	"github.com/spacefleet/spacefleet/lib/email"
	"github.com/spacefleet/spacefleet/lib/queue"
	"github.com/spacefleet/spacefleet/lib/secrets"
	"github.com/spacefleet/spacefleet/lib/tekton"
)

// runWorker is `spacefleet worker` — the long-lived consumer of River
// background jobs. Today it's scaffolding: it opens the River pool, applies
// River's migrations, and starts an empty worker registry. Register real
// jobs by adding a Register* helper in lib/queue and calling it here before
// client.Start.
//
// The two-process split (HTTP `serve` + `worker`) keeps the API stateless
// and horizontally scalable; anything long-running or stateful belongs
// here, not in the request path.
func runWorker(_ []string) {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("worker: load config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rpool, err := queue.Open(ctx, cfg.DatabaseURL, cfg.WorkerConcurrency)
	if err != nil {
		log.Fatalf("worker: open river pool: %v", err)
	}
	defer rpool.Close()

	migrated, err := queue.Migrate(ctx, rpool)
	if err != nil {
		log.Fatalf("worker: migrate: %v", err)
	}
	if len(migrated) == 0 {
		log.Print("worker: river migrations up to date")
	} else {
		for _, m := range migrated {
			log.Printf("worker: applied river migration %d (%s) in %s", m.Version, m.Name, m.Duration)
		}
	}

	// The Tekton install worker needs the domain service (to open sealed
	// credentials and persist install status), so the worker process builds its
	// own ent client + sealer + clusters service — it owns credential access, the
	// same way it owns the email Sender. Job args carry only ids, never secrets.
	sqlDB, entClient, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("worker: open database: %v", err)
	}
	defer func() { _ = entClient.Close(); _ = sqlDB.Close() }()

	sealer, err := secrets.NewSealer(cfg.SecretKey)
	if err != nil {
		log.Fatalf("worker: build secret sealer: %v", err)
	}
	clustersSvc := clusters.NewService(entClient, sealer)

	// Register job workers:
	//   - invite-email: sends org invitation emails (Sender is SMTP when
	//     configured, a no-op otherwise — the API only enqueues when email is on).
	//   - tekton-install: installs Tekton into a cluster on enable.
	workers := queue.NewWorkers()
	queue.AddWorker(workers, &email.InviteEmailWorker{Sender: emailSender(cfg)})
	queue.AddWorker(workers, &tekton.InstallWorker{Store: clustersSvc})

	client, err := queue.NewClient(rpool, queue.Config{
		WorkerMode:  true,
		Concurrency: cfg.WorkerConcurrency,
		Workers:     workers,
		Logger:      slog.Default(),
	})
	if err != nil {
		log.Fatalf("worker: new client: %v", err)
	}

	if err := client.Start(ctx); err != nil {
		log.Fatalf("worker: start: %v", err)
	}
	log.Printf("worker: started (concurrency=%d)", cfg.WorkerConcurrency)

	// Heartbeat loop: emit an info-level log every 30s so deployments
	// without health checks still have a clear "this worker is alive"
	// signal in the log stream.
	go heartbeat(ctx, 30*time.Second)

	waitForSignal()
	log.Println("worker: shutting down")

	cancel() // stop heartbeat

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopCancel()
	if err := client.Stop(stopCtx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("worker: stop: %v", err)
	}
	log.Println("worker: stopped")
}

// emailSender builds the outbound-email transport for job workers: SMTP when
// configured, otherwise a no-op (the API won't enqueue email jobs in that
// case). Keeping the construction here means the worker owns the credentials,
// not the request path.
func emailSender(cfg *config.Config) email.Sender {
	if !cfg.EmailEnabled() {
		return email.Noop{}
	}
	return email.NewSMTP(email.Config{
		Host:     cfg.SMTPHost,
		Port:     cfg.SMTPPort,
		Username: cfg.SMTPUsername,
		Password: cfg.SMTPPassword,
		From:     cfg.SMTPFrom,
		StartTLS: cfg.SMTPStartTLS,
	})
}

// waitForSignal blocks until the process receives SIGINT or SIGTERM —
// the cue to begin a graceful shutdown.
func waitForSignal() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
}

// heartbeat emits a log line every interval until ctx fires. Cheap, but
// invaluable when triaging a worker that mysteriously stopped picking up
// jobs — silence is hard to interpret, presence-with-cadence isn't.
func heartbeat(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			log.Print("worker: heartbeat")
		}
	}
}
