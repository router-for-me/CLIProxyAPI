package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestPostgresConcurrentInitialRefreshClaims(t *testing.T) {
	if os.Getenv("CLIPROXY_TEST_POSTGRES") != "1" || runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires opt-in CLIPROXY_TEST_POSTGRES=1 and a non-root Unix PostgreSQL installation")
	}
	postgres, errLookup := exec.LookPath("postgres")
	if errLookup != nil {
		t.Skip("postgres is not installed")
	}
	bin := filepath.Dir(postgres)
	root, errTemp := os.MkdirTemp("", "cpa-refresh-pg-")
	if errTemp != nil {
		t.Fatal(errTemp)
	}
	t.Cleanup(func() {
		if errRemove := os.RemoveAll(root); errRemove != nil {
			t.Error(errRemove)
		}
	})
	data := filepath.Join(root, "data")
	cmdInit := exec.Command(filepath.Join(bin, "initdb"), "-D", data, "-U", "refresh_test", "-A", "trust", "--no-locale")
	cmdInit.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if output, errRun := cmdInit.CombinedOutput(); errRun != nil {
		t.Fatalf("initdb: %v: %s", errRun, output)
	}
	cmdStart := exec.Command(filepath.Join(bin, "pg_ctl"), "-D", data, "-l", filepath.Join(root, "server.log"), "-o", "-h '' -k "+root, "-w", "start")
	cmdStart.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if output, errRun := cmdStart.CombinedOutput(); errRun != nil {
		serverLog, _ := os.ReadFile(filepath.Join(root, "server.log"))
		t.Fatalf("pg_ctl start: %v: %s\n%s", errRun, output, serverLog)
	}
	t.Cleanup(func() {
		cmdStop := exec.Command(filepath.Join(bin, "pg_ctl"), "-D", data, "-m", "immediate", "-w", "stop")
		cmdStop.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
		if output, errRun := cmdStop.CombinedOutput(); errRun != nil {
			t.Errorf("pg_ctl stop: %v: %s", errRun, output)
		}
	})
	db, errOpen := sql.Open("pgx", "host="+root+" user=refresh_test dbname=postgres sslmode=disable")
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	t.Cleanup(func() {
		if errClose := db.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	const parallel = 16
	db.SetMaxOpenConns(parallel)
	db.SetMaxIdleConns(parallel)
	ctx := context.Background()
	var warm []*sql.Conn
	for index := 0; index < parallel; index++ {
		conn, errConn := db.Conn(ctx)
		if errConn != nil {
			t.Fatal(errConn)
		}
		warm = append(warm, conn)
	}
	for _, conn := range warm {
		if errClose := conn.Close(); errClose != nil {
			t.Fatal(errClose)
		}
	}
	for round := 0; round < 3; round++ {
		start := make(chan struct{})
		results := make(chan error, parallel)
		var wg sync.WaitGroup
		for index := 0; index < parallel; index++ {
			wg.Add(1)
			go func() {
				store := &PostgresStore{db: db, cfg: PostgresStoreConfig{AuthTable: fmt.Sprintf("auths_%d", round)}}
				defer wg.Done()
				<-start
				claimed, errClaim := store.RefreshClaim(ctx, fmt.Sprintf("%064x", index+1))
				if errClaim != nil {
					results <- errClaim
				} else if !claimed {
					results <- fmt.Errorf("distinct grant was not claimed")
				}
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		for errClaim := range results {
			t.Errorf("round %d concurrent initial claim failed: %v", round, errClaim)
		}
	}
}
