package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The load harness is a tool, and an unbudged tool rots: this test runs a small
// load against a real server process (the same binary the deployment uses, over
// the same HTTP+WebSocket surface) so that a change to the protocol or to the
// room lifecycle fails here rather than the first time someone needs a number.
//
// It is deliberately tiny - two matches, three seconds - because CI measures
// correctness, not capacity. The capacity numbers come from
// tools/loadtest-isolated.sh on a dedicated run (docs/M2-LOAD-TESTING.md).

// startServer builds and runs the game server on a free loopback port.
func startServer(t *testing.T) (baseURL string, pid int, stop func()) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; cannot build the target server")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "wordarena-test-server")
	build := exec.Command("go", "build", "-o", bin, "github.com/JoTalbot/words/server/cmd/game")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build target server: %v\n%s", err, out)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	logFile, err := os.Create(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"WORDARENA_ADDR="+addr,
		"WORDARENA_MAX_ROOMS=64",
		"WORDARENA_MAX_SEATS=60",
		// The harness must not spend the test waiting out an admission limit
		// that is not the thing under test.
		"WORDARENA_MUTATIONS_PER_MIN=100000",
	)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	stop = func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
		logFile.Close()
	}

	baseURL = "http://" + addr
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(baseURL + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return baseURL, cmd.Process.Pid, stop
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	body, _ := os.ReadFile(filepath.Join(dir, "server.log"))
	t.Fatalf("server never became healthy; log:\n%s", body)
	return "", 0, nil
}

func TestHarnessRunsASmallLoadAgainstARealServer(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: the harness test starts a real server")
	}
	url, pid, stop := startServer(t)
	defer stop()

	pidFile := filepath.Join(t.TempDir(), "server.pid")
	if err := os.WriteFile(pidFile, []byte(fmt.Sprint(pid)), 0o644); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	rep, err := run(config{
		URL:       url,
		Matches:   2,
		Seats:     2,
		Duration:  3 * time.Second,
		IntentGap: 100 * time.Millisecond,
		Language:  "en",
		PIDFile:   pidFile,
		Quiet:     true,
		Out:       &sb,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if rep.Created != 2 {
		t.Errorf("created %d matches, want 2 (errors %d, throttled %d)", rep.Created, rep.CreateErr, rep.CreateThrottled)
	}
	if rep.CreateErr != 0 || rep.DialErr != 0 {
		t.Errorf("create errors %d, dial errors %d", rep.CreateErr, rep.DialErr)
	}
	if rep.IntentsSent == 0 {
		t.Error("the harness submitted no intents, so it measured nothing")
	}
	if rep.SeatsDropped != 0 {
		t.Errorf("seats dropped = %d, want 0 on a healthy run (a server-side disconnect inside the per-seat budget would be a defect)", rep.SeatsDropped)
	}
	if rep.IntentsAcked != rep.IntentsSent {
		t.Errorf("sent %d intents but got %d acks; the round-trip measurement is incomplete",
			rep.IntentsSent, rep.IntentsAcked)
	}
	if rep.IntentsAccepted == 0 {
		t.Error("no intent was accepted; the load is exercising only the rejection path")
	}
	if rep.ReadErrors != 0 || rep.WriteErrors != 0 {
		t.Errorf("transport errors: read %d write %d", rep.ReadErrors, rep.WriteErrors)
	}
	if rep.Frames == 0 || rep.Bytes == 0 {
		t.Error("no frames were received, so the wire measurement is empty")
	}
	if !strings.Contains(rep.HealthzAfter, "ok") {
		t.Errorf("the server was unhealthy after the run: %s", rep.HealthzAfter)
	}
	if rep.ServerRSSStartMB == 0 || rep.ServerRSSEndMB == 0 {
		t.Error("RSS was not sampled, so the /proc path is broken")
	}
	if rep.ActiveMatchesAfter != 2 {
		t.Errorf("active matches %d after the run, want the 2 that were created", rep.ActiveMatchesAfter)
	}

	// The JSON report is part of the deliverable: a broken report is a broken
	// measurement.
	out := filepath.Join(t.TempDir(), "report.json")
	rep2, err := run(config{
		URL: url, Matches: 1, Seats: 2, Duration: 2 * time.Second, IntentGap: 200 * time.Millisecond,
		Language: "en", JSONOut: out, Quiet: true, Out: &sb,
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var decoded report
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("report is not valid json: %v", err)
	}
	if decoded.Created != rep2.Created || decoded.IntentsSent != rep2.IntentsSent {
		t.Errorf("report on disk disagrees with the run: %+v vs %+v", decoded, rep2)
	}
}

// TestHarnessSeesAnUnreachableTarget documents the failure the harness must
// report rather than hang on: pointing it at nothing must end in an error and a
// zero-clientele report, not a wait.
func TestHarnessFailsFastOnAnUnreachableTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := run(config{
			URL: "http://127.0.0.1:1", Matches: 1, Seats: 2,
			Duration: time.Second, IntentGap: time.Second, Language: "en",
			Quiet: true, Out: nil,
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a load run against an unreachable target reported success")
		}
	case <-ctx.Done():
		t.Fatal("the harness hung on an unreachable target")
	}
}
