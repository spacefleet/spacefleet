package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spacefleet/spacefleet/lib/chartcredentials"
	"github.com/spacefleet/spacefleet/lib/cloudcredentials"
	"github.com/spacefleet/spacefleet/lib/clusters"
	"github.com/spacefleet/spacefleet/lib/config"
	"github.com/spacefleet/spacefleet/lib/db"
	"github.com/spacefleet/spacefleet/lib/deploy"
	"github.com/spacefleet/spacefleet/lib/email"
	"github.com/spacefleet/spacefleet/lib/githubapp"
	"github.com/spacefleet/spacefleet/lib/githubinstallations"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/queue"
	"github.com/spacefleet/spacefleet/lib/secrets"
	"github.com/spacefleet/spacefleet/lib/tekton"
	"github.com/spacefleet/spacefleet/lib/variables"
	"github.com/spacefleet/spacefleet/lib/workflows"
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

	// Jobs build cluster clients too (e.g. serializing a target kubeconfig for a
	// rollout), so the worker enforces the same SSRF endpoint policy as serve.
	k8s.SetEndpointPolicy(k8s.EndpointPolicy{AllowPrivate: cfg.AllowPrivateClusterEndpoints})

	// Root context. River derives every job's work context from the ctx handed
	// to client.Start, so cancelling it hard-aborts all in-flight jobs. It must
	// therefore outlive the graceful client.Stop in shutdownWorker — the
	// deferred cancel here is the last thing to fire on the way out, not part
	// of the shutdown sequence.
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
	chartCredsSvc := chartcredentials.NewService(entClient, sealer)
	cloudCredsSvc := cloudcredentials.NewService(entClient, sealer)

	// GitHub App authenticator + installations service, so a Helm rollout can mint
	// a short-lived token to pull a private-Git chart. Token minting happens here,
	// in ResolveRollout, so the worker (not just serve) must build these. Nil when
	// no App is configured; a configured-but-unparseable key fails fast.
	var ghAuth githubinstallations.Authenticator
	if cfg.GitHubAppEnabled() {
		auth, err := githubapp.New(cfg.GitHubAppID, cfg.GitHubAppPrivateKey, cfg.GitHubAppClientID, cfg.GitHubAppClientSecret)
		if err != nil {
			log.Fatalf("worker: build github app: %v", err)
		}
		ghAuth = auth
	}
	githubInstallsSvc := githubinstallations.NewService(entClient, ghAuth)

	// The workflow run worker resolves each component's run inputs through the
	// shared lib/deploy resolver, built over five deps: the clusters connection
	// resolver, the chart-credentials resolver, the GitHub installations token
	// minter, the cloud-credentials resolver (for a terraform run's cloud auth),
	// and the variables resolver (the env injected into every component job).
	workflowsSvc := workflows.NewService(entClient)
	variablesSvc := variables.NewService(entClient, sealer)
	runResolver := deploy.NewResolver(clustersSvc, chartCredsSvc, githubInstallsSvc, cloudCredsSvc, variablesSvc)

	// Register job workers:
	//   - invite-email: sends org invitation emails (Sender is SMTP when
	//     configured, a no-op otherwise — the API only enqueues when email is on).
	//   - tekton-install: installs Tekton into a cluster on enable.
	//   - workflow-run: executes an application's deploy-workflow DAG, running each
	//     component as a TaskRun on the app's runner cluster (per-component crash-safe
	//     recovery via the component-run label) and reconciling per-step + run status.
	workers := queue.NewWorkers()
	queue.AddWorker(workers, &email.InviteEmailWorker{Sender: emailSender(cfg)})
	queue.AddWorker(workers, &tekton.InstallWorker{Store: clustersSvc})
	queue.AddWorker(workers, workflows.NewWorker(workflowsSvc, runResolver))

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

	// Auxiliary loops (reaper, heartbeat) get their own child context so
	// shutdown can stop them without cancelling ctx itself — which would abort
	// every in-flight job (see the root-context comment above).
	loopCtx, loopCancel := context.WithCancel(ctx)
	defer loopCancel()

	// Reaper: settle workflow runs left stuck "running" by a hard kill (SIGKILL,
	// node loss, OOM) that unwound the worker before its panic-recovery defer could
	// mark them failed — the one case the in-process recovery can't cover. It runs a
	// startup sweep then on a ticker, checking each stuck run's River job via the
	// queue client's JobLive probe; only a run whose job is positively gone is
	// reaped, so a worker still finishing a run is never robbed of it. Wired here
	// (not as a River periodic job) so the probe and the loop live in the one process
	// that owns the River client. liveJob bridges the string job id stored on the run
	// to the int64 River expects; a non-numeric id can't match a River job, so it's
	// treated as gone.
	go workflowsSvc.RunReaper(loopCtx, func(ctx context.Context, jobID string) (bool, error) {
		id, perr := strconv.ParseInt(jobID, 10, 64)
		if perr != nil {
			return false, nil
		}
		return client.JobLive(ctx, id)
	})

	// Heartbeat loop: emit an info-level log every 30s so deployments
	// without health checks still have a clear "this worker is alive"
	// signal in the log stream.
	go heartbeat(loopCtx, 30*time.Second)

	waitForSignal()
	log.Println("worker: shutting down")

	shutdownWorker(client, loopCancel, 30*time.Second, 10*time.Second)
	log.Println("worker: stopped")
}

// jobStopper is the slice of *queue.Client that shutdownWorker needs, split
// out so the shutdown sequence is unit-testable without a River client.
type jobStopper interface {
	Stop(ctx context.Context) error
	StopAndCancel(ctx context.Context) error
}

// shutdownWorker winds the worker down without robbing in-flight jobs of
// their graceful window: the auxiliary loops stop first (jobs keep running),
// then the River client drains for up to drainWindow, and only if that
// expires are job work-contexts cancelled — via StopAndCancel bounded by
// hardWindow, so each job's recovery defers still run before the process
// exits. The root job context must not be cancelled before the drain; doing
// so turns every redeploy into a hard abort of all running jobs and makes
// the graceful Stop dead code.
func shutdownWorker(client jobStopper, stopLoops context.CancelFunc, drainWindow, hardWindow time.Duration) {
	stopLoops() // heartbeat + reaper only — in-flight jobs keep their context

	stopCtx, stopCancel := context.WithTimeout(context.Background(), drainWindow)
	defer stopCancel()
	if err := client.Stop(stopCtx); err != nil {
		log.Printf("worker: graceful stop: %v; cancelling in-flight jobs", err)
		hardCtx, hardCancel := context.WithTimeout(context.Background(), hardWindow)
		defer hardCancel()
		if err := client.StopAndCancel(hardCtx); err != nil {
			log.Printf("worker: hard stop: %v", err)
		}
	}
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
