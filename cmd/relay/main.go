package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	gosmtp "github.com/emersion/go-smtp"
	"go.uber.org/zap"

	"smtp-mailersend-relay/internal/config"
	"smtp-mailersend-relay/internal/dispatch"
	"smtp-mailersend-relay/internal/dlq"
	"smtp-mailersend-relay/internal/httpops"
	"smtp-mailersend-relay/internal/logging"
	"smtp-mailersend-relay/internal/metrics"
	"smtp-mailersend-relay/internal/mimeparse"
	"smtp-mailersend-relay/internal/queue"
	"smtp-mailersend-relay/internal/smtpserver"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return runServe(nil)
	}
	switch args[0] {
	case "serve":
		return runServe(args[1:])
	case "migrate":
		return runMigrate(args[1:])
	case "dlq":
		return runDLQ(args[1:])
	default:
		return fmt.Errorf("unknown command %q (expected: serve|migrate|dlq)", args[0])
	}
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.LoadFromEnv()
	if err != nil {
		return err
	}
	if err := cfg.ValidateServe(); err != nil {
		return err
	}

	logger, err := logging.New(cfg.LogLevel)
	if err != nil {
		return err
	}
	defer logger.Sync()

	workerID := fmt.Sprintf("%s-%d", hostnameOr("relay"), os.Getpid())
	store, err := queue.OpenSQLite(cfg.DBPath, cfg.DBMaxOpenConns, cfg.DBMaxIdleConns, workerID)
	if err != nil {
		return err
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("db migration failed: %w", err)
	}

	m := metrics.New()
	transformer := mimeparse.NewTransformer(cfg.MailersendEnableCustomHeaders)
	logger.Info("mailersend account limits resolved",
		zap.String("plan", cfg.MailersendAccountPlan),
		zap.Bool("bulk_api_supported", cfg.MailersendBulkAPISupported),
		zap.Bool("custom_headers_supported", cfg.MailersendCustomHeadersSupported),
		zap.Int("bulk_api_max_messages_per_request", cfg.MailersendBulkAPIMaxMessagesPerRequest),
		zap.Int("bulk_api_max_requests_per_min", cfg.MailersendBulkAPIMaxRequestsPerMin),
		zap.Int("effective_batch_max_count", cfg.EffectiveBatchMaxCount()),
	)

	sender, err := dispatch.NewMailerSendSender(cfg.MailersendAPIKey, cfg.MailersendBaseURL, cfg.MailersendTimeout)
	if err != nil {
		return err
	}
	limiter := dispatch.NewRequestLimiter(cfg.MailersendBulkAPIMaxRequestsPerMin)
	disp := dispatch.New(dispatch.Config{
		Workers:             cfg.DispatcherWorkers,
		QueueClaimLimit:     cfg.QueueClaimLimit,
		QueueLeaseTimeout:   cfg.QueueLeaseTimeout,
		BatchMaxCount:       cfg.EffectiveBatchMaxCount(),
		BatchMaxBytes:       cfg.BatchMaxBytes,
		BatchFlushInterval:  cfg.BatchFlushInterval,
		RetryMaxAttempts:    cfg.RetryMaxAttempts,
		RequeueStaleEvery:   cfg.RequeueStaleInterval,
		QueueDepthPollEvery: 2 * time.Second,
	}, logger, m, store, sender, limiter)

	// Recover stale processing leases at startup.
	if n, err := store.RequeueStaleProcessing(ctx, time.Now().UTC(), cfg.QueueLeaseTimeout); err != nil {
		logger.Warn("startup stale requeue failed", zap.Error(err))
	} else if n > 0 {
		logger.Info("startup stale jobs requeued", zap.Int64("count", n))
		m.RequeueStaleRecoveries.Add(float64(n))
	}

	httpSrv := httpops.New(cfg.HTTPListenAddr, logger, m, store, disp)
	smtpSrv, err := smtpserver.New(cfg, logger, m, store, transformer)
	if err != nil {
		return err
	}

	rootCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	disp.Start(rootCtx)

	errCh := make(chan error, 2)
	go func() {
		if err := httpSrv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http ops server: %w", err)
		}
	}()
	go func() {
		if err := smtpSrv.Start(); err != nil && !errors.Is(err, gosmtp.ErrServerClosed) {
			errCh <- fmt.Errorf("smtp server: %w", err)
		}
	}()

	select {
	case <-rootCtx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		logger.Error("service failed", zap.Error(err))
		cancel()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shutdownCancel()
	_ = smtpSrv.Shutdown(shutdownCtx)
	_ = httpSrv.Shutdown(shutdownCtx)
	disp.Wait()
	return nil
}

func runMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.LoadFromEnv()
	if err != nil {
		return err
	}

	store, err := queue.OpenSQLite(cfg.DBPath, cfg.DBMaxOpenConns, cfg.DBMaxIdleConns, "migration")
	if err != nil {
		return err
	}
	defer store.Close()

	if err := store.Migrate(context.Background()); err != nil {
		return err
	}
	fmt.Println("migration complete")
	return nil
}

func runDLQ(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: relay dlq export|replay")
	}
	switch args[0] {
	case "export":
		return runDLQExport(args[1:])
	case "replay":
		return runDLQReplay(args[1:])
	default:
		return fmt.Errorf("unknown dlq subcommand %q (expected: export|replay)", args[0])
	}
}

func runDLQExport(args []string) error {
	fs := flag.NewFlagSet("dlq export", flag.ContinueOnError)
	out := fs.String("out", "./dlq-export.jsonl", "output file path")
	format := fs.String("format", "jsonl", "export format (jsonl)")
	limit := fs.Int("limit", 100000, "max DLQ rows to export")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.ToLower(strings.TrimSpace(*format)) != "jsonl" {
		return fmt.Errorf("unsupported format %q", *format)
	}

	cfg, err := config.LoadFromEnv()
	if err != nil {
		return err
	}
	store, err := queue.OpenSQLite(cfg.DBPath, cfg.DBMaxOpenConns, cfg.DBMaxIdleConns, "dlq-export")
	if err != nil {
		return err
	}
	defer store.Close()

	svc := dlq.New(store)
	n, err := svc.ExportJSONL(context.Background(), *out, *limit)
	if err != nil {
		return err
	}
	fmt.Printf("exported %d records to %s\n", n, *out)
	return nil
}

func runDLQReplay(args []string) error {
	fs := flag.NewFlagSet("dlq replay", flag.ContinueOnError)
	idsCSV := fs.String("ids", "", "comma-separated DLQ job IDs to replay")
	fromFile := fs.String("from-file", "", "file containing IDs (one per line) or JSONL export")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*idsCSV) == "" && strings.TrimSpace(*fromFile) == "" {
		return fmt.Errorf("at least one of --ids or --from-file is required")
	}

	var ids []string
	if strings.TrimSpace(*idsCSV) != "" {
		for _, part := range strings.Split(*idsCSV, ",") {
			p := strings.TrimSpace(part)
			if p != "" {
				ids = append(ids, p)
			}
		}
	}
	if strings.TrimSpace(*fromFile) != "" {
		fromIDs, err := dlq.ReadIDsFromFile(*fromFile)
		if err != nil {
			return err
		}
		ids = append(ids, fromIDs...)
	}
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		return fmt.Errorf("no valid ids provided")
	}

	cfg, err := config.LoadFromEnv()
	if err != nil {
		return err
	}
	store, err := queue.OpenSQLite(cfg.DBPath, cfg.DBMaxOpenConns, cfg.DBMaxIdleConns, "dlq-replay")
	if err != nil {
		return err
	}
	defer store.Close()

	svc := dlq.New(store)
	n, err := svc.ReplayIDs(context.Background(), ids)
	if err != nil {
		return err
	}
	fmt.Printf("requeued %d records\n", n)
	return nil
}

func uniqueIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func hostnameOr(def string) string {
	h, err := os.Hostname()
	if err != nil || strings.TrimSpace(h) == "" {
		return def
	}
	return h
}
