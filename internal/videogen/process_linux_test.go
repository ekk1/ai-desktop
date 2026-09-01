//go:build linux

package videogen

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestCLIExecutorRunsPrepareThenMainAndStopsProcessGroup(t *testing.T) {
	executor := NewCLIExecutor()
	request := fixtureCLIRunRequest(t, `printf 'prepare-out\n'; printf prepared > "$OUTPUT_DIR/prepared"`, `test -f "$OUTPUT_DIR/prepared"; trap 'exit 0' TERM; printf 'main-out\n'; printf 'main-err\n' >&2; printf ready > "$OUTPUT_DIR/ready"; while :; do :; done`)
	completed := runCLIAsync(executor, request)
	status := waitForCLIProcess(t, executor, request.AttemptID, CLIStateRunning)
	waitForPath(t, filepath.Join(request.OutputDir, "ready"))
	if err := executor.Stop(context.Background(), request.AttemptID); err != nil {
		t.Fatal(err)
	}
	result := <-completed
	if result.err != nil || result.result.State != CLIStateStopped || result.result.ExitCode != 0 {
		t.Fatalf("result = %#v, error = %v", result.result, result.err)
	}
	if err := waitForProcessGroupGone(status.PID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := executor.SnapshotLog(request.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(snapshot.Data), "prepare-out\nmain-out\nmain-err\n"; got != want {
		t.Fatalf("combined raw log = %q, want %q", got, want)
	}
	if err := executor.Stop(context.Background(), request.AttemptID); !errors.Is(err, ErrCLIAttemptNotRunning) {
		t.Fatalf("Stop ended attempt error = %v, want ErrCLIAttemptNotRunning", err)
	}
}

func TestCLIExecutorPrepareFailureDoesNotRunMain(t *testing.T) {
	request := fixtureCLIRunRequest(t, `printf prepare; exit 7`, `printf main > "$OUTPUT_DIR/main-ran"`)
	result, err := NewCLIExecutor().Run(context.Background(), request)
	if err == nil || result.State != CLIStateFailed || result.ExitCode != 7 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if _, statErr := os.Stat(filepath.Join(request.OutputDir, "main-ran")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("main command ran after prepare failure: %v", statErr)
	}
}

func TestCLIExecutorTimeoutKillsWholeProcessGroup(t *testing.T) {
	executor := NewCLIExecutor()
	request := fixtureCLIRunRequest(t, "", `trap '' TERM; (trap '' TERM; while :; do sleep 1; done) & wait`)
	request.Timeout = 100 * time.Millisecond
	// This exceeds the post-KILL confirmation bound. A bound calculated before
	// grace would expire before KILL is sent and make Stop return an error.
	request.StopGrace = 1100 * time.Millisecond
	completed := runCLIAsync(executor, request)
	status := waitForCLIProcess(t, executor, request.AttemptID, CLIStateRunning)
	result := <-completed
	if !errors.Is(result.err, context.DeadlineExceeded) || result.result.State != CLIStateFailed {
		t.Fatalf("result = %#v, error = %v", result.result, result.err)
	}
	if err := waitForProcessGroupGone(status.PID); err != nil {
		t.Fatal(err)
	}
}

func TestCLIExecutorStopCancellationStillKillsAndReapsGroup(t *testing.T) {
	executor := NewCLIExecutor()
	request := fixtureCLIRunRequest(t, "", `trap '' TERM; (trap '' TERM; while :; do sleep 1; done) & wait`)
	request.StopGrace = time.Second
	completed := runCLIAsync(executor, request)
	status := waitForCLIProcess(t, executor, request.AttemptID, CLIStateRunning)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := executor.Stop(ctx, request.AttemptID); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop error = %v, want context.Canceled", err)
	}
	result := <-completed
	if result.result.State != CLIStateStopped {
		t.Fatalf("result = %#v, error = %v", result.result, result.err)
	}
	if err := waitForProcessGroupGone(status.PID); err != nil {
		t.Fatal(err)
	}
}

func TestCLIExecutorStopKillsTermIgnoringChildAfterShellExitsZero(t *testing.T) {
	executor := NewCLIExecutor()
	request := fixtureCLIRunRequest(t, "", `trap 'exit 0' TERM; (trap '' TERM; printf '%s' "$BASHPID" > "$OUTPUT_DIR/child"; exec sleep 30) & while :; do sleep 30; done`)
	request.StopGrace = 30 * time.Millisecond
	completed := runCLIAsync(executor, request)
	status := waitForCLIProcess(t, executor, request.AttemptID, CLIStateRunning)
	waitForPath(t, filepath.Join(request.OutputDir, "child"))
	if err := executor.Stop(context.Background(), request.AttemptID); err != nil {
		t.Fatal(err)
	}
	result := <-completed
	if result.err != nil || result.result.State != CLIStateStopped || result.result.ExitCode != 0 {
		t.Fatalf("result = %#v, error = %v", result.result, result.err)
	}
	if err := waitForProcessGroupGone(status.PID); err != nil {
		t.Fatal(err)
	}
}

func TestCLIExecutorStopsChildAfterLeaderExitsWithLogPipeOpen(t *testing.T) {
	executor := NewCLIExecutor()
	request := fixtureCLIRunRequest(t, "", `(trap '' TERM; while :; do sleep 1; done) & printf leader-exited; exit 0`)
	completed := runCLIAsync(executor, request)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := executor.SnapshotLog(request.AttemptID)
		if err == nil && strings.Contains(string(snapshot.Data), "leader-exited") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- executor.Stop(context.Background(), request.AttemptID) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop blocked after leader exit while child held command log pipes")
	}
	if result := <-completed; result.result.State != CLIStateStopped {
		t.Fatalf("result = %#v, error = %v", result.result, result.err)
	}
}

func TestCLIExecutorStopBeforeProcessStartPreventsProcessGroupLaunch(t *testing.T) {
	executor := NewCLIExecutor()
	request := fixtureCLIRunRequest(t, "", `printf ran > "$OUTPUT_DIR/ran"; trap '' TERM; while :; do :; done`)
	request.Timeout = 200 * time.Millisecond
	blocked := &blockingDeadlineContext{
		Context: context.Background(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	completed := make(chan cliRunOutcome, 1)
	go func() {
		result, err := executor.Run(blocked, request)
		completed <- cliRunOutcome{result: result, err: err}
	}()
	<-blocked.entered
	status, err := executor.Status(request.AttemptID)
	if err != nil || status.PID != 0 || !status.Running {
		t.Fatalf("pre-start status = %#v, error = %v", status, err)
	}
	stopContext, cancel := context.WithCancel(context.Background())
	cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- executor.Stop(stopContext, request.AttemptID) }()
	waitForCLIState(t, executor, request.AttemptID, CLIStateStopping)
	close(blocked.release)
	if err := <-stopped; !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop error = %v, want context.Canceled", err)
	}
	outcome := <-completed
	if outcome.err != nil || outcome.result.State != CLIStateStopped || outcome.result.PID != 0 {
		t.Fatalf("result = %#v, error = %v", outcome.result, outcome.err)
	}
	if _, err := os.Stat(filepath.Join(request.OutputDir, "ran")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command launched after pre-start Stop: %v", err)
	}
}

func TestCLIExecutorRunsAttemptOnlyOnceAndClonesStatus(t *testing.T) {
	executor := NewCLIExecutor()
	request := fixtureCLIRunRequest(t, "", `trap 'exit 0' TERM; printf x >> "$OUTPUT_DIR/count"; while :; do sleep 1; done`)
	request.Env["PRIVATE"] = "original"
	completed := runCLIAsync(executor, request)
	waitForCLIProcess(t, executor, request.AttemptID, CLIStateRunning)
	waitForPath(t, filepath.Join(request.OutputDir, "count"))
	if _, err := executor.Run(context.Background(), request); !errors.Is(err, ErrCLIAttemptExists) {
		t.Fatalf("duplicate Run error = %v, want ErrCLIAttemptExists", err)
	}
	status, err := executor.Status(request.AttemptID)
	if err != nil || status.AttemptID != request.AttemptID || status.PID <= 0 || !status.Running || status.StartedAt.IsZero() {
		t.Fatal("attempt status missing")
	}
	status.Request.Env["PRIVATE"] = "mutated"
	again, err := executor.Status(request.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Request.Env["PRIVATE"]; got != "original" {
		t.Fatalf("Status exposed request map: %q", got)
	}
	if err := executor.Stop(context.Background(), request.AttemptID); err != nil {
		t.Fatal(err)
	}
	<-completed
	contents, err := os.ReadFile(filepath.Join(request.OutputDir, "count"))
	if err != nil || string(contents) != "x" {
		t.Fatalf("execution count marker = %q, error = %v", contents, err)
	}
}

func TestCLIExecutorResolvesOutputParentsWithinWorkspace(t *testing.T) {
	request := fixtureCLIRunRequest(t, "", `printf '\x1a\x45\xdf\xa3' > "$OUTPUT_PATH"`)
	realParent := filepath.Join(request.OutputDir, "real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(request.OutputDir, "linked")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}
	request.OutputPath = filepath.Join(linkedParent, "result.webm")
	request.Env["OUTPUT_PATH"] = request.OutputPath

	result, err := NewCLIExecutor().Run(context.Background(), request)
	if err != nil || result.State != CLIStateSucceeded || result.OutputPath != request.OutputPath {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestOpenAbsoluteDirectoryNoFollowRejectsSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "swap")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if fd, err := openAbsoluteDirectoryNoFollow(filepath.Join(link, "child")); err == nil {
		_ = syscall.Close(fd)
		t.Fatal("absolute no-follow traversal accepted a symlink ancestor")
	}
}

func TestCLIExecutorRejectsOutputDirectoryOutsideWorkspace(t *testing.T) {
	request := fixtureCLIRunRequest(t, "", `printf '\x1a\x45\xdf\xa3' > "$OUTPUT_PATH"`)
	request.OutputDir = t.TempDir()
	request.OutputPath = filepath.Join(request.OutputDir, "result.webm")
	request.Env["OUTPUT_DIR"] = request.OutputDir
	request.Env["OUTPUT_PATH"] = request.OutputPath

	result, err := NewCLIExecutor().Run(context.Background(), request)
	if err == nil || result.State != CLIStateFailed {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestCLIExecutorRejectsOutputParentSymlinkEscape(t *testing.T) {
	request := fixtureCLIRunRequest(t, "", `printf '\x1a\x45\xdf\xa3' > "$OUTPUT_PATH"`)
	outside := t.TempDir()
	linkedParent := filepath.Join(request.OutputDir, "linked")
	if err := os.Symlink(outside, linkedParent); err != nil {
		t.Fatal(err)
	}
	request.OutputPath = filepath.Join(linkedParent, "result.webm")
	request.Env["OUTPUT_PATH"] = request.OutputPath

	result, err := NewCLIExecutor().Run(context.Background(), request)
	if err == nil || result.State != CLIStateFailed {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestCLIExecutorShutdownStopsAllAttemptsAndRejectsNewRuns(t *testing.T) {
	executor := NewCLIExecutor()
	first := fixtureCLIRunRequest(t, "", `trap 'exit 0' TERM; while :; do sleep 1; done`)
	second := fixtureCLIRunRequest(t, "", `trap 'exit 0' TERM; while :; do sleep 1; done`)
	firstDone := runCLIAsync(executor, first)
	secondDone := runCLIAsync(executor, second)
	waitForCLIProcess(t, executor, first.AttemptID, CLIStateRunning)
	waitForCLIProcess(t, executor, second.AttemptID, CLIStateRunning)
	if err := executor.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if (<-firstDone).result.State != CLIStateStopped || (<-secondDone).result.State != CLIStateStopped {
		t.Fatal("Shutdown did not stop every active attempt")
	}
	third := fixtureCLIRunRequest(t, "", `exit 0`)
	if _, err := executor.Run(context.Background(), third); !errors.Is(err, ErrCLIExecutorShutdown) {
		t.Fatalf("Run after Shutdown error = %v, want ErrCLIExecutorShutdown", err)
	}
}

func TestCLIExecutorShutdownStopsPrepareToMainHandoff(t *testing.T) {
	executor := NewCLIExecutor()
	entered := make(chan struct{})
	release := make(chan struct{})
	executor.beforeMain = func() { close(entered); <-release }
	request := fixtureCLIRunRequest(t, `printf prepared > "$OUTPUT_DIR/prepared"`, `printf main > "$OUTPUT_DIR/main"`)
	completed := runCLIAsync(executor, request)
	<-entered
	status, err := executor.Status(request.AttemptID)
	if err != nil || !status.Running || status.PID != 0 {
		t.Fatalf("handoff status = %#v, error = %v", status, err)
	}
	shutdown := make(chan error, 1)
	go func() { shutdown <- executor.Shutdown(context.Background()) }()
	waitForCLIState(t, executor, request.AttemptID, CLIStateStopping)
	close(release)
	if err := <-shutdown; err != nil {
		t.Fatal(err)
	}
	result := <-completed
	if result.err != nil || result.result.State != CLIStateStopped {
		t.Fatalf("result = %#v, error = %v", result.result, result.err)
	}
	if _, err := os.Stat(filepath.Join(request.OutputDir, "main")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("main launched after Shutdown: %v", err)
	}
}

func TestCLIExecutorRetainsOffsetLogsAndSavesOnlyOnRequest(t *testing.T) {
	executor := NewCLIExecutor()
	request := fixtureCLIRunRequest(t, "", `printf 'log-data'; printf '\x1a\x45\xdf\xa3' > "$OUTPUT_PATH"`)
	result, err := executor.Run(context.Background(), request)
	if err != nil || result.OutputPath != request.OutputPath {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	before, chunks, cancel, err := executor.SubscribeLog(request.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if before.StartOffset != 0 || before.EndOffset != 8 || string(before.Data) != "log-data" {
		t.Fatalf("retained log = %#v", before)
	}
	if _, open := <-chunks; open {
		t.Fatal("canceled log subscription remained open")
	}
	entries, err := filepath.Glob(filepath.Join(request.WorkDir, "*.log"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("executor automatically saved logs: %v, %v", entries, err)
	}
	destination := filepath.Join(t.TempDir(), "manual.log")
	savedPath, err := executor.SaveLog(request.AttemptID, destination)
	if err != nil {
		t.Fatal(err)
	}
	if savedPath != destination {
		t.Fatalf("saved path = %q, want %q", savedPath, destination)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "log-data" {
		t.Fatalf("saved log = %q, error = %v", contents, err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved log mode = %v, error = %v", info.Mode().Perm(), err)
	}
}

func TestCLIExecutorSaveLogRejectsDirectoryAndSymlinkDestinations(t *testing.T) {
	executor := NewCLIExecutor()
	request := fixtureCLIRunRequest(t, "", `printf log; printf '\x1a\x45\xdf\xa3' > "$OUTPUT_PATH"`)
	if _, err := executor.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if _, err := executor.SaveLog(request.AttemptID, directory); err == nil {
		t.Fatal("SaveLog accepted a directory destination")
	}
	target := filepath.Join(t.TempDir(), "target.log")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.SaveLog(request.AttemptID, link); err == nil {
		t.Fatal("SaveLog accepted a symlink destination")
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("symlink target = %q, error = %v", contents, err)
	}
}

func TestCLIExecutorAcceptsSupportedDeclaredOutputs(t *testing.T) {
	formats := []struct {
		name, mediaType, extension, bytes string
		size                              int64
	}{
		{"webm", "video/webm", ".webm", `\x1a\x45\xdf\xa3`, 4},
		{"avi", "video/x-msvideo", ".avi", `RIFF\x04\x00\x00\x00AVI `, 12},
		{"webp", "image/webp", ".webp", `RIFF\x04\x00\x00\x00WEBP`, 12},
		{"png", "image/png", ".png", `\x89PNG\r\n\x1a\n`, 8},
		{"jpeg", "image/jpeg", ".jpg", `\xff\xd8\xff\xe0`, 4},
		{"jpeg alias", "image/jpeg", ".jpeg", `\xff\xd8\xff\xe0`, 4},
		{"mp4", "video/mp4", ".mp4", `\x00\x00\x00\x0cftypisom`, 12},
		{"mov", "video/quicktime", ".mov", `\x00\x00\x00\x0cftypqt  `, 12},
	}
	for _, format := range formats {
		t.Run(format.name, func(t *testing.T) {
			request := fixtureCLIRunRequest(t, "", `printf '`+format.bytes+`' > "$OUTPUT_PATH"`)
			request.OutputMediaType = format.mediaType
			request.OutputExtension = format.extension
			request.OutputPath = filepath.Join(request.OutputDir, "result"+format.extension)
			request.Env["OUTPUT_PATH"] = request.OutputPath
			result, err := NewCLIExecutor().Run(context.Background(), request)
			if err != nil || result.OutputPath != request.OutputPath || result.OutputSize != format.size || result.State != CLIStateSucceeded {
				t.Fatalf("result = %#v, error = %v", result, err)
			}
		})
	}
}

func TestCLIExecutorRejectsMissingUnsafeOversizeAndMismatchedOutputs(t *testing.T) {
	tests := []struct {
		name   string
		change func(*CLIRunRequest)
		main   string
	}{
		{"missing", nil, `:`},
		{"empty", nil, `: > "$OUTPUT_PATH"`},
		{"symlink", nil, `printf '\x1a\x45\xdf\xa3' > "$WORKSPACE_DIR/target.webm"; ln -s "$WORKSPACE_DIR/target.webm" "$OUTPUT_PATH"`},
		{"outside outputs", func(request *CLIRunRequest) {
			request.OutputPath = filepath.Join(request.WorkDir, "outside.webm")
			request.Env["OUTPUT_PATH"] = request.OutputPath
		}, `printf '\x1a\x45\xdf\xa3' > "$OUTPUT_PATH"`},
		{"over limit", func(request *CLIRunRequest) { request.MaxOutputBytes = 4 }, `printf '\x1a\x45\xdf\xa3x' > "$OUTPUT_PATH"`},
		{"extension mismatch", func(request *CLIRunRequest) { request.OutputExtension = ".mp4" }, `printf '\x1a\x45\xdf\xa3' > "$OUTPUT_PATH"`},
		{"mime mismatch", func(request *CLIRunRequest) { request.OutputMediaType = "video/mp4" }, `printf '\x00\x00\x00\x0cftypisom' > "$OUTPUT_PATH"`},
		{"magic mismatch", nil, `printf nope > "$OUTPUT_PATH"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := fixtureCLIRunRequest(t, "", test.main)
			if test.change != nil {
				test.change(&request)
			}
			result, err := NewCLIExecutor().Run(context.Background(), request)
			if err == nil || result.State != CLIStateFailed || result.ExitCode != 0 {
				t.Fatalf("result = %#v, error = %v", result, err)
			}
		})
	}
}

func TestCLIExecutorUnknownLifecycleAndLogAPIsUseSentinel(t *testing.T) {
	executor := NewCLIExecutor()
	if _, err := executor.Status("missing"); !errors.Is(err, ErrCLIAttemptNotFound) {
		t.Fatalf("Status unknown error = %v", err)
	}
	if err := executor.Stop(context.Background(), "missing"); !errors.Is(err, ErrCLIAttemptNotRunning) {
		t.Fatalf("Stop unknown error = %v", err)
	}
	if _, err := executor.SnapshotLog("missing"); !errors.Is(err, ErrCLIAttemptNotFound) {
		t.Fatalf("SnapshotLog unknown error = %v", err)
	}
	if _, _, _, err := executor.SubscribeLog("missing"); !errors.Is(err, ErrCLIAttemptNotFound) {
		t.Fatalf("SubscribeLog unknown error = %v", err)
	}
	if _, err := executor.SaveLog("missing", filepath.Join(t.TempDir(), "log")); !errors.Is(err, ErrCLIAttemptNotFound) {
		t.Fatalf("SaveLog unknown error = %v", err)
	}
}

func TestCLIExecutorRejectsInvalidAttemptIdentityAndEnvironment(t *testing.T) {
	tests := []struct {
		name   string
		change func(*CLIRunRequest)
	}{
		{"attempt ID", func(request *CLIRunRequest) { request.AttemptID = "not-a-generated-id" }},
		{"environment key NUL", func(request *CLIRunRequest) { request.Env["BAD\x00KEY"] = "value" }},
		{"environment value NUL", func(request *CLIRunRequest) { request.Env["BAD_VALUE"] = "bad\x00value" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := fixtureCLIRunRequest(t, "", `printf '\x1a\x45\xdf\xa3' > "$OUTPUT_PATH"`)
			test.change(&request)
			if _, err := NewCLIExecutor().Run(context.Background(), request); err == nil {
				t.Fatal("Run accepted invalid execution identity or environment")
			}
		})
	}
}

func TestCLIExecutorRejectsRelativeWorkDirAndMaxOutputOverflow(t *testing.T) {
	for _, change := range []func(*CLIRunRequest){
		func(request *CLIRunRequest) { request.WorkDir = "relative" },
		func(request *CLIRunRequest) { request.MaxOutputBytes = math.MaxInt64 },
	} {
		request := fixtureCLIRunRequest(t, "", `printf '\x1a\x45\xdf\xa3' > "$OUTPUT_PATH"`)
		change(&request)
		if _, err := NewCLIExecutor().Run(context.Background(), request); err == nil {
			t.Fatal("Run accepted an unsafe direct execution request")
		}
	}
}

type cliRunOutcome struct {
	result CLIRunResult
	err    error
}

func fixtureCLIRunRequest(t *testing.T, prepare, main string) CLIRunRequest {
	t.Helper()
	root := t.TempDir()
	outputDir := filepath.Join(root, "outputs")
	if err := os.Mkdir(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(t.Name()+root)))
	outputPath := filepath.Join(outputDir, "result.webm")
	return CLIRunRequest{
		AttemptID:      digest[:32],
		PrepareCommand: prepare,
		Command:        main,
		WorkspaceRoot:  root,
		WorkDir:        root,
		Env: map[string]string{
			"WORKSPACE_DIR": root,
			"OUTPUT_DIR":    outputDir,
			"OUTPUT_PATH":   outputPath,
		},
		Timeout:         3 * time.Second,
		StopGrace:       100 * time.Millisecond,
		LogBufferBytes:  1024,
		OutputDir:       outputDir,
		OutputPath:      outputPath,
		OutputMediaType: "video/webm",
		OutputExtension: ".webm",
		MaxOutputBytes:  1 << 20,
	}
}

func runCLIAsync(executor *CLIExecutor, request CLIRunRequest) <-chan cliRunOutcome {
	done := make(chan cliRunOutcome, 1)
	go func() {
		result, err := executor.Run(context.Background(), request)
		done <- cliRunOutcome{result: result, err: err}
	}()
	return done
}

func waitForCLIProcess(t *testing.T, executor *CLIExecutor, attemptID string, want CLIRunState) CLIRunStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if status, err := executor.Status(attemptID); err == nil && status.State == want && status.PID > 0 {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	status, _ := executor.Status(attemptID)
	t.Fatalf("attempt %q did not reach %s with a PID: %#v", attemptID, want, status)
	return CLIRunStatus{}
}

func waitForCLIState(t *testing.T, executor *CLIExecutor, attemptID string, want CLIRunState) CLIRunStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if status, err := executor.Status(attemptID); err == nil && status.State == want {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	status, _ := executor.Status(attemptID)
	t.Fatalf("attempt %q did not reach %s: %#v", attemptID, want, status)
	return CLIRunStatus{}
}

func waitForProcessGroupGone(pid int) error {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("process group %d remains", pid)
}

func waitForPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect path %q: %v", path, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("path %q was not created", path)
}

type blockingDeadlineContext struct {
	context.Context
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (ctx *blockingDeadlineContext) Deadline() (time.Time, bool) {
	ctx.once.Do(func() { close(ctx.entered) })
	<-ctx.release
	return time.Time{}, false
}

func TestCLIExecutorSortedEnvironmentOverridesParent(t *testing.T) {
	t.Setenv("VIDEO_EXECUTOR_VALUE", "parent")
	request := fixtureCLIRunRequest(t, "", `printf '%s' "$VIDEO_EXECUTOR_VALUE"; printf '\x1a\x45\xdf\xa3' > "$OUTPUT_PATH"`)
	request.Env["VIDEO_EXECUTOR_VALUE"] = "override"
	executor := NewCLIExecutor()
	if _, err := executor.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	snapshot, err := executor.SnapshotLog(request.AttemptID)
	if err != nil || strings.TrimSpace(string(snapshot.Data)) != "override" {
		t.Fatalf("environment log = %q, error = %v", snapshot.Data, err)
	}
}

func TestCLIExecutorDefaultsEmptyWorkDirToWorkspaceRoot(t *testing.T) {
	request := fixtureCLIRunRequest(t, "", `printf '%s' "$PWD"; printf '\x1a\x45\xdf\xa3' > "$OUTPUT_PATH"`)
	request.WorkDir = ""
	executor := NewCLIExecutor()
	if _, err := executor.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	snapshot, err := executor.SnapshotLog(request.AttemptID)
	if err != nil || string(snapshot.Data) != request.WorkspaceRoot {
		t.Fatalf("command work directory log = %q, error = %v", snapshot.Data, err)
	}
}
