// Command indexer runs the Anandan H1 Arc indexer and local-ledger reconciler.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/chaincfg"
	"github.com/gnanam1990/snapfall/daemon/internal/indexer"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

func main() {
	deploymentPath := flag.String("deployment", "../deployments/arc-testnet.json", "machine-readable A1 deployment config")
	dbPath := flag.String("db", "snapfall.db", "shared SQLite database")
	once := flag.Bool("once", false, "sync and reconcile once, then exit")
	interval := flag.Duration("interval", time.Second, "poll interval")
	chunkSize := flag.Uint64("chunk-size", 2_000, "maximum inclusive block range per RPC request")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(*deploymentPath, *dbPath, *once, *interval, *chunkSize, log); err != nil {
		log.Error("indexer stopped", "err", err)
		os.Exit(1)
	}
}

func run(deploymentPath, dbPath string, once bool, interval time.Duration, chunkSize uint64, log *slog.Logger) error {
	if interval <= 0 {
		return fmt.Errorf("interval must be positive")
	}
	deployment, err := chaincfg.Load(deploymentPath, os.LookupEnv)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	source, err := indexer.NewRPCSource(deployment.Network.RPCURL, nil)
	if err != nil {
		return err
	}
	syncer, err := indexer.New(source, st, indexer.Config{
		ChainID: deployment.Network.ChainID, Addresses: deployment.IndexerAddresses(),
		StartBlock: deployment.Network.StartBlock, ConfirmationDepth: deployment.Network.ConfirmationDepth,
		ChunkSize: chunkSize,
	})
	if err != nil {
		return err
	}
	reconciler, err := indexer.NewReconciler(st, deployment.Network.ChainID)
	if err != nil {
		return err
	}

	log.Info("H1 indexer ready", "network", deployment.Network.Name, "chain_id", deployment.Network.ChainID,
		"start_block", deployment.Network.StartBlock, "confirmation_depth", deployment.Network.ConfirmationDepth)
	for {
		result, err := syncer.SyncOnce(ctx)
		if err != nil {
			if once {
				return err
			}
			log.Error("H1 sync failed", "err", err)
		} else {
			reconciliation, err := reconciler.Run(ctx)
			if err != nil {
				if once {
					return err
				}
				log.Error("reconciliation failed", "err", err)
			} else if result.RawLogs > 0 || reconciliation.HasMismatch {
				log.Info("H1 sync complete", "through_block", result.ThroughBlock, "next_block", result.NextBlock,
					"raw_logs", result.RawLogs, "events", result.Events,
					"reconciliation_mismatches", len(reconciliation.Alerts))
			}
		}
		if once {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
