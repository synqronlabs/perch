package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/synqronlabs/perch/internal/blob"
	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/hub"
	"github.com/synqronlabs/perch/internal/ingest"
	"github.com/synqronlabs/perch/internal/store"
	cli "github.com/urfave/cli/v3"
)

func backupCommand() *cli.Command {
	return &cli.Command{
		Name:      "backup",
		Usage:     "write a consistent SQLite backup",
		ArgsUsage: "<destination.db>",
		Flags:     []cli.Flag{configFlag(defaultConfigPath())},
		Action:    runBackup,
	}
}

func runBackup(ctx context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() != 1 {
		return errors.New("usage: perch backup [flags] <destination.db>")
	}
	return withConfigStore(cmd.String("config"), func(_ config.Config, st *store.Store) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		if err := st.Backup(ctx, cmd.Args().First()); err != nil {
			return err
		}
		fmt.Printf("backup written to %s\n", cmd.Args().First())
		return nil
	})
}

func reconcileCommand() *cli.Command {
	return &cli.Command{
		Name:   "reconcile",
		Usage:  "force one S3 bucket reconciliation pass",
		Flags:  []cli.Flag{configFlag(defaultConfigPath())},
		Action: runReconcile,
	}
}

func runReconcile(ctx context.Context, cmd *cli.Command) error {
	return withConfigStore(cmd.String("config"), func(cfg config.Config, st *store.Store) error {
		if cfg.Storage.Mode != config.StorageS3 {
			return errors.New("reconcile requires storage.mode=s3")
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		s3, err := blob.NewS3(ctx, cfg.Storage.S3)
		if err != nil {
			return err
		}
		ingester := ingest.New(cfg, st, s3, hub.New(), newLogger(cfg.Log))
		if err := ingester.ReconcileOnce(ctx); err != nil {
			return err
		}
		fmt.Println("S3 reconciliation complete")
		return nil
	})
}

func gcCommand() *cli.Command {
	return &cli.Command{
		Name:  "gc",
		Usage: "remove orphan blobs, old dead letters, and S3 tombstones",
		Flags: []cli.Flag{
			configFlag(defaultConfigPath()),
			&cli.BoolFlag{Name: "dry-run", Usage: "report what would be removed"},
			&cli.BoolFlag{Name: "s3-delete", Usage: "delete S3 objects for deleted messages"},
			&cli.DurationFlag{Name: "dead-letter-age", Value: 30 * 24 * time.Hour, Usage: "delete dead letters older than this"},
			&cli.BoolFlag{Name: "vacuum", Value: true, Usage: "run incremental vacuum after cleanup"},
			&cli.BoolFlag{Name: "spool", Value: true, Usage: "enforce storage.s3.spool_max_bytes"},
		},
		Action: runGC,
	}
}

func runGC(ctx context.Context, cmd *cli.Command) error {
	dryRun := cmd.Bool("dry-run")
	return withConfigStore(cmd.String("config"), func(cfg config.Config, st *store.Store) error {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()

		localCount, localBytes, err := st.GCLocalOrphans(ctx, dryRun)
		if err != nil {
			return err
		}
		fmt.Printf("local orphan blobs: %d (%d bytes)%s\n", localCount, localBytes, drySuffix(dryRun))
		if cfg.Storage.Mode == config.StorageS3 {
			tombstones, err := st.S3Tombstones(ctx)
			if err != nil {
				return err
			}
			var s3Bytes int64
			for _, tombstone := range tombstones {
				s3Bytes += tombstone.Size
			}
			fmt.Printf("S3 tombstones: %d (%d bytes)%s\n", len(tombstones), s3Bytes, drySuffix(dryRun))
			if cmd.Bool("s3-delete") {
				s3, err := blob.NewS3(ctx, cfg.Storage.S3)
				if err != nil {
					return err
				}
				for _, tombstone := range tombstones {
					if dryRun {
						continue
					}
					if err := s3.Delete(ctx, tombstone.Ref); err != nil {
						return err
					}
					if err := st.DeleteBlob(ctx, tombstone.ID); err != nil {
						return err
					}
				}
			}
			if cmd.Bool("spool") {
				if dryRun {
					spoolBytes, spoolCount, err := st.SpoolStats(ctx)
					if err != nil {
						return err
					}
					fmt.Printf("spool: %d objects, %d bytes (limit %d) (dry run)\n", spoolCount, spoolBytes, cfg.Storage.S3.SpoolMaxBytes)
				} else {
					evicted, freed, err := st.EnforceSpoolLimit(ctx, cfg.Storage.S3.SpoolMaxBytes)
					if err != nil {
						return err
					}
					fmt.Printf("spool evicted: %d (%d bytes)\n", evicted, freed)
				}
			}
		}
		if !dryRun {
			removed, err := st.PurgeDeadLetters(ctx, time.Now().Add(-cmd.Duration("dead-letter-age")))
			if err != nil {
				return err
			}
			fmt.Printf("dead letters removed: %d\n", removed)
			if cmd.Bool("vacuum") {
				if err := st.IncrementalVacuum(ctx); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func drySuffix(dry bool) string {
	if dry {
		return " (dry run)"
	}
	return ""
}
