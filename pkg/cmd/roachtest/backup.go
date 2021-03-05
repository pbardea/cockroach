// Copyright 2018 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cockroachdb/cockroach/pkg/jobs"
	"github.com/cockroachdb/cockroach/pkg/storage/cloudimpl"
	"github.com/cockroachdb/cockroach/pkg/util/timeutil"
	"github.com/cockroachdb/cockroach/pkg/util/version"
	"github.com/cockroachdb/errors"
)

// The following env variable names match those specified in the TeamCity
// configuration for the nightly roachtests. Any changes must be made to both
// references of the name.
const (
	KMSRegionAEnvVar = "AWS_KMS_REGION_A"
	KMSRegionBEnvVar = "AWS_KMS_REGION_B"
	KMSKeyARNAEnvVar = "AWS_KMS_KEY_ARN_A"
	KMSKeyARNBEnvVar = "AWS_KMS_KEY_ARN_B"

	// rows2TB is the number of rows to import to load 2TB of data (when replicated).
	rows2TiB   = 65_104_166
	rows100GiB = rows2TiB / 20
	rows30GiB  = rows2TiB / 66
	rows3GiB   = rows30GiB / 10
)

func registerBackup(r *testRegistry) {
	importBankDataSplit := func(ctx context.Context, rows, ranges int, t *test, c *cluster) string {
		dest := c.name
		// Randomize starting with encryption-at-rest enabled.
		c.encryptAtRandom = true

		if local {
			dest += fmt.Sprintf("%d", timeutil.Now().UnixNano())
		}

		c.Put(ctx, workload, "./workload")
		c.Put(ctx, cockroach, "./cockroach")

		// NB: starting the cluster creates the logs dir as a side effect,
		// needed below.
		c.Start(ctx, t)
		c.Run(ctx, c.All(), `./workload csv-server --port=8081 &> logs/workload-csv-server.log < /dev/null &`)
		time.Sleep(time.Second) // wait for csv server to open listener

		importArgs := []string{
			"./workload", "fixtures", "import", "bank",
			"--db=bank", "--payload-bytes=10240", fmt.Sprintf("--ranges=%d", ranges), "--csv-server", "http://localhost:8081",
			fmt.Sprintf("--rows=%d", rows), "--seed=1", "{pgurl:1}",
		}
		c.Run(ctx, c.Node(1), importArgs...)

		return dest
	}

	importBankData := func(ctx context.Context, rows int, t *test, c *cluster) string {
		return importBankDataSplit(ctx, rows, 0 /* ranges */, t, c)
	}

	backup2TBSpec := makeClusterSpec(10)
	r.Add(testSpec{
		Name:       fmt.Sprintf("backup/2TB/%s", backup2TBSpec),
		Owner:      OwnerBulkIO,
		Cluster:    backup2TBSpec,
		MinVersion: "v2.1.0",
		Run: func(ctx context.Context, t *test, c *cluster) {
			rows := rows2TiB
			if local {
				rows = 100
			}
			dest := importBankData(ctx, rows, t, c)
			m := newMonitor(ctx, c)
			m.Go(func(ctx context.Context) error {
				t.Status(`running backup`)
				c.Run(ctx, c.Node(1), `./cockroach sql --insecure -e "
				BACKUP bank.bank TO 'gs://cockroachdb-backup-testing/`+dest+`'"`)
				return nil
			})
			m.Wait()
		},
	})

	// backupNodeRestartSpec runs a backup and randomly shuts down a node
	// during the backup.
	backupNodeRestartSpec := makeClusterSpec(4)
	r.Add(testSpec{
		Name:       fmt.Sprintf("backup/nodeShutdown/%s", backupNodeRestartSpec),
		Owner:      OwnerBulkIO,
		Cluster:    backupNodeRestartSpec,
		MinVersion: "v21.1.0",
		Run: func(ctx context.Context, t *test, c *cluster) {
			// ~100 GiB aught to be enough since this isn't a
			// performance test.
			rows := rows100GiB
			if local {
				// Needs to be sufficiently large to give each
				// processor a good chunk of works so the job
				// doesn't complete immediately.
				// Even with this size, there is still a chance
				// this test will return false-positives when
				// run locally.
				rows = rows3GiB
			}

			ranges := 100
			dest := importBankDataSplit(ctx, rows, ranges, t, c)
			// The backup job is run on the gateway, so this node will be the
			// coordinator initially.
			gatewayNode := 2
			// Select 2 nodes, a target that will be shutdown, and a watcher that can
			// check the status of the job.

			// TODO(pbardea): The watcher's connection when querying the job
			// status mysteriously hangs when targetting node 1.
			targetNode := 3 // 1 + rand.Intn(c.spec.NodeCount)
			watcherNode := 1 + (targetNode)%c.spec.NodeCount
			target := c.Node(targetNode)
			t.l.Printf("test has chosen gateway node %d, shutdown target node %d, and watcher node %d",
				gatewayNode, targetNode, watcherNode)

			jobIDCh := make(chan string, 1)

			m := newMonitor(ctx, c)
			m.Go(func(ctx context.Context) error {
				t.Status(`running backup`)
				query := `BACKUP bank.bank TO 'nodelocal://1/` + dest + `' WITH DETACHED`

				gatewayDB := c.Conn(ctx, gatewayNode)
				defer gatewayDB.Close()

				var jobID string
				if err := gatewayDB.QueryRowContext(ctx, query).Scan(&jobID); err != nil {
					return errors.Wrap(err, "running backup statement")
				}
				t.l.Printf("started running backup job with ID %s on node %d", jobID, gatewayNode)
				jobIDCh <- jobID
				close(jobIDCh)

				pollInterval := 30 * time.Second
				if local {
					pollInterval = 5 * time.Second
				}
				ticker := time.NewTicker(pollInterval)

				watcherDB := c.Conn(ctx, watcherNode)
				defer watcherDB.Close()

				var status string
				for {
					select {
					case <-ticker.C:
						err := watcherDB.QueryRowContext(ctx, `SELECT status FROM [SHOW JOBS] WHERE job_id=$1`, jobID).Scan(&status)
						if err != nil {
							return errors.Wrap(err, "getting the job status")
						}
						jobStatus := jobs.Status(status)
						switch jobStatus {
						case jobs.StatusSucceeded:
							t.Status("backup job completed")
							return nil
						case jobs.StatusRunning:
							t.l.Printf("backup job %s still running, waiting to succeed", jobID)
						default:
							// Waiting for job to complete.
							return errors.Newf("unexpectedly found backup job %s in state %s", jobID, status)
						}
					case <-ctx.Done():
						return errors.Wrap(ctx.Err(), "context cancelled while waiting for job to finish")
					}
				}
			})

			m.Go(func(ctx context.Context) error {
				jobID := <-jobIDCh

				t.l.Printf(`shutting down node %s`, target)

				// Shutdown a node after a bit, and keep it shutdown for the remainder
				// of the backup.
				timeToWait := 20 * time.Second
				if local {
					timeToWait = 8 * time.Second
				}
				timer := timeutil.Timer{}
				timer.Reset(timeToWait)
				select {
				case <-ctx.Done():
					return errors.Wrapf(ctx.Err(), "stopping test, did not shutdown node")
				case <-timer.C:
					timer.Read = true
				}

				// Sanity check that the job is still running.
				watcherDB := c.Conn(ctx, watcherNode)
				defer watcherDB.Close()

				var status string
				err := watcherDB.QueryRowContext(ctx, `SELECT status FROM [SHOW JOBS] WHERE job_id=$1`, jobID).Scan(&status)
				if err != nil {
					return errors.Wrap(err, "getting the job status")
				}
				jobStatus := jobs.Status(status)
				if jobStatus != jobs.StatusRunning {
					return errors.Newf("job too fast! job got to state %s before the target node could be shutdown",
						status)
				}

				if err := c.StopE(ctx, target); err != nil {
					return errors.Wrapf(err, "could not stop node %s", target)
				}
				t.l.Printf("stopped node %s", target)

				return nil
			})

			// Before calling `m.Wait()`, do some cleanup.
			if err := m.g.Wait(); err != nil {
				t.Fatal(err)
			}

			// NB: the roachtest harness checks that at the end of the test, all nodes
			// that have data also have a running process.
			t.Status(fmt.Sprintf("restarting %s (node restart test is done)\n", target))
			if err := c.StartE(ctx, target); err != nil {
				t.Fatal(errors.Wrapf(err, "could not restart node %s", target))
			}

			m.Wait()
		},
	})

	KMSSpec := makeClusterSpec(3)
	r.Add(testSpec{
		Name:       fmt.Sprintf("backup/KMS/%s", KMSSpec.String()),
		Owner:      OwnerBulkIO,
		Cluster:    KMSSpec,
		MinVersion: "v20.2.0",
		Run: func(ctx context.Context, t *test, c *cluster) {
			if cloud == gce {
				t.Skip("backupKMS roachtest is only configured to run on AWS", "")
			}

			// ~10GiB - which is 30Gib replicated.
			rows := rows30GiB
			if local {
				rows = 100
			}
			dest := importBankData(ctx, rows, t, c)

			conn := c.Conn(ctx, 1)
			m := newMonitor(ctx, c)
			m.Go(func(ctx context.Context) error {
				_, err := conn.ExecContext(ctx, `
					CREATE DATABASE restoreA;
					CREATE DATABASE restoreB;
				`)
				return err
			})
			m.Wait()

			var kmsURIA, kmsURIB string
			var err error
			m = newMonitor(ctx, c)
			m.Go(func(ctx context.Context) error {
				t.Status(`running encrypted backup`)
				kmsURIA, err = getAWSKMSURI(KMSRegionAEnvVar, KMSKeyARNAEnvVar)
				if err != nil {
					return err
				}

				kmsURIB, err = getAWSKMSURI(KMSRegionBEnvVar, KMSKeyARNBEnvVar)
				if err != nil {
					return err
				}

				kmsOptions := fmt.Sprintf("KMS=('%s', '%s')", kmsURIA, kmsURIB)
				_, err := conn.ExecContext(ctx,
					`BACKUP bank.bank TO 'nodelocal://1/kmsbackup/`+dest+`' WITH `+kmsOptions)
				return err
			})
			m.Wait()

			// Restore the encrypted BACKUP using each of KMS URI A and B separately.
			m = newMonitor(ctx, c)
			m.Go(func(ctx context.Context) error {
				t.Status(`restore using KMSURIA`)
				if _, err := conn.ExecContext(ctx,
					`RESTORE bank.bank FROM $1 WITH into_db=restoreA, kms=$2`,
					`nodelocal://1/kmsbackup/`+dest, kmsURIA,
				); err != nil {
					return err
				}

				t.Status(`restore using KMSURIB`)
				if _, err := conn.ExecContext(ctx,
					`RESTORE bank.bank FROM $1 WITH into_db=restoreB, kms=$2`,
					`nodelocal://1/kmsbackup/`+dest, kmsURIB,
				); err != nil {
					return err
				}

				t.Status(`fingerprint`)
				fingerprint := func(db string) (string, error) {
					var b strings.Builder

					query := fmt.Sprintf("SHOW EXPERIMENTAL_FINGERPRINTS FROM TABLE %s.%s", db, "bank")
					rows, err := conn.QueryContext(ctx, query)
					if err != nil {
						return "", err
					}
					defer rows.Close()
					for rows.Next() {
						var name, fp string
						if err := rows.Scan(&name, &fp); err != nil {
							return "", err
						}
						fmt.Fprintf(&b, "%s: %s\n", name, fp)
					}

					return b.String(), rows.Err()
				}

				originalBank, err := fingerprint("bank")
				if err != nil {
					return err
				}
				restoreA, err := fingerprint("restoreA")
				if err != nil {
					return err
				}
				restoreB, err := fingerprint("restoreB")
				if err != nil {
					return err
				}

				if originalBank != restoreA {
					return errors.Errorf("got %s, expected %s while comparing restoreA with originalBank", restoreA, originalBank)
				}
				if originalBank != restoreB {
					return errors.Errorf("got %s, expected %s while comparing restoreB with originalBank", restoreB, originalBank)
				}

				return nil
			})
			m.Wait()
		},
	})

	// backupTPCC continuously runs TPCC, takes a full backup after some time,
	// and incremental after more time. It then restores the two backups and
	// verifies them with a fingerprint.
	r.Add(testSpec{
		Name:    `backupTPCC`,
		Owner:   OwnerBulkIO,
		Cluster: makeClusterSpec(3),
		Timeout: 1 * time.Hour,
		Run: func(ctx context.Context, t *test, c *cluster) {
			// Randomize starting with encryption-at-rest enabled.
			c.encryptAtRandom = true
			c.Put(ctx, cockroach, "./cockroach")
			c.Put(ctx, workload, "./workload")
			c.Start(ctx, t)
			conn := c.Conn(ctx, 1)

			duration := 5 * time.Minute
			if local {
				duration = 5 * time.Second
			}
			warehouses := 10

			backupDir := "gs://cockroachdb-backup-testing/" + c.name
			// Use inter-node file sharing on 20.1+.
			if r.buildVersion.AtLeast(version.MustParse(`v20.1.0-0`)) {
				backupDir = "nodelocal://1/" + c.name
			}
			fullDir := backupDir + "/full"
			incDir := backupDir + "/inc"

			t.Status(`workload initialization`)
			cmd := []string{fmt.Sprintf(
				"./workload init tpcc --warehouses=%d {pgurl:1-%d}",
				warehouses, c.spec.NodeCount,
			)}
			if !t.buildVersion.AtLeast(version.MustParse("v20.2.0")) {
				cmd = append(cmd, "--deprecated-fk-indexes")
			}
			c.Run(ctx, c.Node(1), cmd...)

			m := newMonitor(ctx, c)
			m.Go(func(ctx context.Context) error {
				_, err := conn.ExecContext(ctx, `
					CREATE DATABASE restore_full;
					CREATE DATABASE restore_inc;
				`)
				return err
			})
			m.Wait()

			t.Status(`run tpcc`)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			cmdDone := make(chan error)
			go func() {
				cmd := fmt.Sprintf(
					"./workload run tpcc --warehouses=%d {pgurl:1-%d}",
					warehouses, c.spec.NodeCount,
				)

				cmdDone <- c.RunE(ctx, c.Node(1), cmd)
			}()

			select {
			case <-time.After(duration):
			case <-ctx.Done():
				return
			}

			// Use a time slightly in the past to avoid "cannot specify timestamp in the future" errors.
			tFull := fmt.Sprint(timeutil.Now().Add(time.Second * -2).UnixNano())
			m = newMonitor(ctx, c)
			m.Go(func(ctx context.Context) error {
				t.Status(`full backup`)
				_, err := conn.ExecContext(ctx,
					`BACKUP tpcc.* TO $1 AS OF SYSTEM TIME `+tFull,
					fullDir,
				)
				return err
			})
			m.Wait()

			t.Status(`continue tpcc`)
			select {
			case <-time.After(duration):
			case <-ctx.Done():
				return
			}

			tInc := fmt.Sprint(timeutil.Now().Add(time.Second * -2).UnixNano())
			m = newMonitor(ctx, c)
			m.Go(func(ctx context.Context) error {
				t.Status(`incremental backup`)
				_, err := conn.ExecContext(ctx,
					`BACKUP tpcc.* TO $1 AS OF SYSTEM TIME `+tInc+` INCREMENTAL FROM $2`,
					incDir,
					fullDir,
				)
				if err != nil {
					return err
				}

				// Backups are done, make sure workload is still running.
				select {
				case err := <-cmdDone:
					// Workload exited before it should have.
					return err
				default:
					return nil
				}
			})
			m.Wait()

			m = newMonitor(ctx, c)
			m.Go(func(ctx context.Context) error {
				t.Status(`restore full`)
				if _, err := conn.ExecContext(ctx,
					`RESTORE tpcc.* FROM $1 WITH into_db='restore_full'`,
					fullDir,
				); err != nil {
					return err
				}

				t.Status(`restore incremental`)
				if _, err := conn.ExecContext(ctx,
					`RESTORE tpcc.* FROM $1, $2 WITH into_db='restore_inc'`,
					fullDir,
					incDir,
				); err != nil {
					return err
				}

				t.Status(`fingerprint`)
				// TODO(adityamaru): Pull the fingerprint logic into a utility method
				// which can be shared by multiple roachtests.
				fingerprint := func(db string, asof string) (string, error) {
					var b strings.Builder

					var tables []string
					rows, err := conn.QueryContext(
						ctx,
						fmt.Sprintf("SELECT table_name FROM [SHOW TABLES FROM %s] ORDER BY table_name", db),
					)
					if err != nil {
						return "", err
					}
					defer rows.Close()
					for rows.Next() {
						var name string
						if err := rows.Scan(&name); err != nil {
							return "", err
						}
						tables = append(tables, name)
					}

					for _, table := range tables {
						fmt.Fprintf(&b, "table %s\n", table)
						query := fmt.Sprintf("SHOW EXPERIMENTAL_FINGERPRINTS FROM TABLE %s.%s", db, table)
						if asof != "" {
							query = fmt.Sprintf("SELECT * FROM [%s] AS OF SYSTEM TIME %s", query, asof)
						}
						rows, err = conn.QueryContext(ctx, query)
						if err != nil {
							return "", err
						}
						defer rows.Close()
						for rows.Next() {
							var name, fp string
							if err := rows.Scan(&name, &fp); err != nil {
								return "", err
							}
							fmt.Fprintf(&b, "%s: %s\n", name, fp)
						}
					}

					return b.String(), rows.Err()
				}

				tpccFull, err := fingerprint("tpcc", tFull)
				if err != nil {
					return err
				}
				tpccInc, err := fingerprint("tpcc", tInc)
				if err != nil {
					return err
				}
				restoreFull, err := fingerprint("restore_full", "")
				if err != nil {
					return err
				}
				restoreInc, err := fingerprint("restore_inc", "")
				if err != nil {
					return err
				}

				if tpccFull != restoreFull {
					return errors.Errorf("got %s, expected %s", restoreFull, tpccFull)
				}
				if tpccInc != restoreInc {
					return errors.Errorf("got %s, expected %s", restoreInc, tpccInc)
				}

				return nil
			})
			m.Wait()
		},
	})

}

func getAWSKMSURI(regionEnvVariable, keyIDEnvVariable string) (string, error) {
	q := make(url.Values)
	expect := map[string]string{
		"AWS_ACCESS_KEY_ID":     cloudimpl.AWSAccessKeyParam,
		"AWS_SECRET_ACCESS_KEY": cloudimpl.AWSSecretParam,
		regionEnvVariable:       cloudimpl.KMSRegionParam,
	}
	for env, param := range expect {
		v := os.Getenv(env)
		if v == "" {
			return "", errors.Newf("env variable %s must be present to run the KMS test", env)
		}
		q.Add(param, v)
	}

	// Get AWS Key ARN from env variable.
	keyARN := os.Getenv(keyIDEnvVariable)
	if keyARN == "" {
		return "", errors.Newf("env variable %s must be present to run the KMS test", keyIDEnvVariable)
	}

	// Set AUTH to implicit
	q.Add(cloudimpl.AuthParam, cloudimpl.AuthParamSpecified)
	correctURI := fmt.Sprintf("aws:///%s?%s", keyARN, q.Encode())

	return correctURI, nil
}
