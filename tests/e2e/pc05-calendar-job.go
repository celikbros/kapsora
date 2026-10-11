// Run only through the opt-in PC05 checker. This helper reads scheduler execution windows;
// it never runs a job or changes a settlement.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type window struct {
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

func main() {
	if err := run(); err != nil {
		// Connection errors can contain private host details. Never print the underlying error.
		fmt.Fprintln(os.Stderr, "read-only scheduler evidence unavailable")
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 || os.Getenv("KAPSORA_DATABASE_URL") == "" {
		return errors.New("missing private input")
	}
	day, err := time.Parse("2006-01-02", os.Args[1])
	if err != nil || day.Format("2006-01-02") != os.Args[1] {
		return errors.New("invalid UTC day")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("KAPSORA_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT started_at, finished_at
		  FROM system.job_run
		 WHERE job_code = 'billing.reconcile'
		   AND status = 'SUCCEEDED'
		   AND started_at >= $1 AND started_at < $2
		 ORDER BY started_at`, day, day.AddDate(0, 0, 1))
	if err != nil {
		return err
	}
	defer rows.Close()
	windows := make([]window, 0)
	for rows.Next() {
		var item window
		if err := rows.Scan(&item.StartedAt, &item.FinishedAt); err != nil {
			return err
		}
		windows = append(windows, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Windows []window `json:"windows"`
	}{windows})
}
