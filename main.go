package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"gitvault/internal/app"
	"gitvault/internal/backup"
	"gitvault/internal/config"
	"gitvault/internal/desktop"
	"gitvault/internal/github"
	"gitvault/internal/secrets"
	"gitvault/internal/store"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func main() {
	if handled, code := dispatchCredentialHelper(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// dispatchCredentialHelper must stay before run: private Git helper invocations
// must not initialize the UI, database, logger, or secret-store adapter.
func dispatchCredentialHelper(args []string, stdin io.Reader, stdout, stderr io.Writer) (bool, int) {
	return backup.HelperMain(context.Background(), args, stdin, stdout, stderr)
}

func run() error {
	credentials, err := secrets.NewNative("gitvault", secrets.NativeDevelopmentBuild)
	if err != nil {
		return fmt.Errorf("initialize native credential adapter: %w", err)
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("resolve application data directory: %w", err)
	}
	dataDir := filepath.Join(configDir, "GitVault")
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(dataDir, "gitvault.sqlite3"))
	if err != nil {
		return fmt.Errorf("open local application database: %w", err)
	}
	workflow, err := app.NewLocalWorkflowWithDiscovery(ctx, s, buildLocalEngine(s), credentials, buildGitHubDiscovery)
	if err != nil {
		credentials.CloseLeases()
		return errors.Join(fmt.Errorf("start local workflow: %w", err), s.Close())
	}
	current, err := s.CurrentConfig(ctx)
	if err != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		return errors.Join(err, workflow.Close(closeCtx), s.Close())
	}
	backend := desktop.NewEventBackend(workflow.Snapshot(), workflow.Updates(), workflow.Submit, current.Config().Limits.UIBatchSize)
	view := desktop.NewWithBackend(backend)
	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:     "GitVault",
			Width:     1000,
			Height:    700,
			MinWidth:  800,
			MinHeight: 560,
			StateKey:  "main",
			Content:   ui.View(view.View),
		})
	})
	runErr := mygo.App.Run()
	view.ClearSensitiveDrafts()
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	closeErr := workflow.Close(closeCtx)
	cancel()
	return errors.Join(runErr, closeErr, s.Close())
}

func buildGitHubDiscovery(revision config.Revision) (app.GitHubDiscoverer, error) {
	values := revision.Config()
	options := github.DefaultOptions()
	options.MaxRepositories = values.Limits.DiscoveryRepositories
	options.ResponseBytes = values.Limits.HTTPResponseBytes
	options.RequestTimeout = values.Timeouts.HTTPRequest.Value()
	options.MaxAttempts = values.Retry.MaxAttempts
	options.RateWait = values.GitHub.RateLimitWait.Value()
	options.InitialBackoff = values.Retry.InitialBackoff.Value()
	options.MaxBackoff = values.Retry.MaxBackoff.Value()
	return github.New(options)
}

func buildLocalEngine(s *store.Store) app.LocalEngineBuilder {
	return func(root store.RootBinding, revision config.Revision, progress chan<- backup.ProgressEvent) (app.SnapshotEngine, error) {
		values := revision.Config()
		identity := s.Identity()
		scratch := filepath.Join(identity.DataDir, "git-scratch")
		if err := os.MkdirAll(scratch, 0700); err != nil {
			return nil, fmt.Errorf("create private Git scratch directory: %w", err)
		}
		runnerOptions := backup.DefaultGitRunnerOptions()
		runnerOptions.CommandTimeout = values.Timeouts.Git.Value()
		runnerOptions.MachineOutputBytes = int(values.Limits.MachineOutputBytes)
		runnerOptions.DiagnosticBytes = int(values.Limits.DiagnosticBytes)
		runner, err := backup.NewGitRunner(scratch, runnerOptions)
		if err != nil {
			return nil, fmt.Errorf("construct controlled Git runner: %w", err)
		}
		limits := backup.ManifestLimits{
			ManifestBytes:      values.Limits.ManifestBytes,
			ReferenceCount:     values.Limits.ReferenceCount,
			ReferenceNameBytes: values.Limits.ReferenceNameBytes,
			MetadataBytes:      values.Limits.ManifestBytes,
			BundleBytes:        values.Limits.BundleBytes,
		}
		engine, err := backup.NewEngine(backup.EngineOptions{
			Root:                     root.Path,
			Runner:                   runner,
			Limits:                   limits,
			VerificationScratchBytes: values.Limits.VerificationScratchBytes,
			InstallationID:           identity.InstallationID,
			StoreID:                  identity.StoreID,
			ApplicationData:          identity.DataDir,
			Progress:                 progress,
		})
		if err != nil {
			return nil, fmt.Errorf("construct local recovery engine: %w", err)
		}
		return engine, nil
	}
}
