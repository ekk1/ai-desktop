# Task 10 Fix Round 4 Report

## Outcome

Completed the remaining video API behavior, response-data, strict JSON, SSE lifecycle, workspace, Tail result-selection, and error-mapping coverage. The only production change is the minimal mapping of `videogen.ErrCLIExecutorShutdown` to a redacted HTTP 500 `internal_error`; the new failing contract test demonstrated that it previously returned HTTP 400.

The pre-existing unstaged `docs/superpowers/plans/2026-08-31-video-workspace.md` change was preserved and was not staged or modified as part of this fix.

## Requirement checklist

| Requirement | Test name | Concrete assertions |
|---|---|---|
| Config GET decodes HTTP, CLI, and Tail configuration | `TestVideoConfigAPIReadsAllPresetKindsAndRejectsEscapingCLIOutput` | Decodes `videoconfig.Config`; asserts one HTTP provider with ID/base URL, one CLI preset with ID/output path/env, and one Tail preset with ID/extension. |
| Config PUT rejects escaping CLI output path | `TestVideoConfigAPIReadsAllPresetKindsAndRejectsEscapingCLIOutput` | PUTs `outputs/../../outside.webm`; asserts 400 and that persisted `output_relative_path` remains `outputs/contract.webm`. |
| Batch list `q` and `folder` filters return the matching records | `TestVideoBatchAndItemAPIContract` | Creates two real batches; decodes each list response; asserts exactly one batch and its expected ID for `q=ALPHA` and `folder=other`. |
| Batch detail reports preset availability and safe, stable Asset summaries | `TestVideoBatchAndItemAPIContract` | Imports and activates two real image Assets, references both from a real Item, decodes `videoBatchResponse`, asserts `preset_available=true`, two summaries ascending by Asset ID, non-empty display names, and raw response absence of `stored_name`, `b64_json`, and the test root. |
| Item update persists prompt | `TestVideoBatchAndItemAPIContract` | Decodes Item PUT response, asserts ID and `prompt="persisted prompt"`, then reads the real Service and asserts the stored prompt. |
| Item move persists canonical order | `TestVideoBatchAndItemAPIContract` | Moves the first Item down; decodes Batch and asserts exact item IDs at Orders 0/1; a second downward move asserts 409. |
| Item DELETE removes the Item and compacts Order | `TestVideoBatchAndItemAPIContract` | Asserts 204, then reads Service and asserts only the other Item remains at Order 0. |
| Batch execute returns accepted Attempt identities/count | `TestVideoBatchAndItemAPIContract` | Decodes 202 response; asserts exactly one Attempt, a 32-hex-length ID, and matching Batch/Item IDs. |
| Batch PUT persists response fields | `TestVideoBatchAndItemAPIContract` | Decodes updated Batch; asserts title, folder, concurrency, frame count, FPS, and exact compact common params. |
| Batch DELETE makes subsequent GET missing | `TestVideoBatchAndItemAPIContract` | Deletes both test batches with 204 and asserts each subsequent GET is 404. |
| Batch SSE initial snapshot, state, 5 ms heartbeat, request cancellation, unsubscribe | `TestVideoBatchSSESnapshotStateHeartbeatAndCancellation` | Uses a real Manager; decodes snapshot Type/Batch ID/empty Attempts, starts a real Attempt and decodes matching state event, observes `: heartbeat`, cancels request context, asserts handler returns within one second, and asserts the real Manager subscriber map is empty. |
| Attempt GET/cancel/retry return the correct old/new records | `TestVideoAttemptAPIExecutesGetsAndCancels` | Decodes accepted Attempt and GET identity/non-terminal state; decodes cancel with same ID and `cancelled`; decodes retry with a distinct 32-character ID, same Batch/Item IDs, and `queued`, then cancels the retry. |
| Real CLI log save returns a relative location without root leakage | `TestVideoCLIWorkspaceSaveCleanupAndNoClearEndpointContract` | Starts a real CLI Attempt, waits for real executor running state, decodes `workspace_path`, asserts `video-logs/<attempt>.log`, absence of test root, and existence under DataDir. |
| CLI active cleanup conflicts; terminal cleanup removes workspace | `TestVideoCLIWorkspaceSaveCleanupAndNoClearEndpointContract` | Asserts active DELETE is 409, decodes cancel as terminal `cancelled`, asserts terminal DELETE is 204, and `os.Stat` reports workspace absent. |
| Browser clear has no server API | `TestVideoCLIWorkspaceSaveCleanupAndNoClearEndpointContract` | Asserts both GET and POST `/logs/clear` return 404. |
| Tail SSE snapshot, state, 5 ms heartbeat, request cancellation, unsubscribe | `TestVideoTailSSESaveAssetSelectionAndCancelContract` | Uses a real TailExtractor; decodes snapshot identity/source/preset and queued/running state, observes heartbeat, releases real command, decodes succeeded state/output ID/completion time, cancels context, asserts handler return, and asserts Tail subscriber map is empty. |
| Tail log save returns a relative location | `TestVideoTailSSESaveAssetSelectionAndCancelContract` | Decodes save response, asserts `videos/tail-logs/<id>.log`, absence of root, and actual file existence under DataDir. |
| Successful PNG Tail result is selected through existing Asset state API | `TestVideoTailSSESaveAssetSelectionAndCancelContract` | POSTs `/api/v1/assets/{id}/state`, decodes active Asset with matching ID; GET decodes matching active `image/png` Asset. |
| Tail cancel response is terminal | `TestVideoTailSSESaveAssetSelectionAndCancelContract` | Starts a second real Tail command, waits for executor running state, decodes cancel response, and asserts matching ID, `cancelled`, and non-nil completion time. |
| Unknown fields are rejected on every video JSON write shape | `TestVideoConfigAPIReadsAllPresetKindsAndRejectsEscapingCLIOutput`, `TestVideoBatchAndItemAPIContract`, `TestVideoAttemptAPIExecutesGetsAndCancels`, `TestVideoCLIWorkspaceSaveCleanupAndNoClearEndpointContract`, `TestVideoTailSSESaveAssetSelectionAndCancelContract` | Asserts 400 for Config PUT; Batch POST/PUT; Item collection POST/Item PUT/move/Item execute; Batch execute; Attempt retry/cancel; CLI save; Tail create/cancel/save. |
| Domain not-found, active conflict, move conflict, validation, PathError, and executor shutdown status mapping | `TestWriteVideoAPIErrorStatusAndRedactionContract`, `TestVideoHandlersMapStorageNotFoundAndValidationErrors`, `TestVideoBatchAndItemAPIContract`, `TestVideoCLIWorkspaceSaveCleanupAndNoClearEndpointContract` | Decodes error envelopes and asserts domain missing=404, active/move=409, validation=400, PathError=500 `storage_error`, executor shutdown=500 `internal_error`; real handlers assert missing/validation/storage statuses; absolute test root is absent from error bodies. |

## TDD evidence

### RED

Command:

```text
go test ./internal/web -run 'TestWriteVideoAPIErrorStatusAndRedactionContract|TestVideoHandlersMapStorageNotFoundAndValidationErrors' -count=1
```

Relevant failure before the production change:

```text
--- FAIL: TestWriteVideoAPIErrorStatusAndRedactionContract/executor_shutdown
    video_error_test.go:37: status=400 ...
FAIL
```

This was the expected failure: `writeVideoAPIError` sent `ErrCLIExecutorShutdown` through the generic validation branch instead of treating the unavailable process executor as an internal failure.

An earlier Item integration run also returned 400 because imported Assets intentionally default to `archive`. Root-cause tracing showed this was invalid fixture setup, not a handler defect; the test now explicitly activates both real Assets before creating references.

### GREEN

Command:

```text
go test ./internal/web -run 'TestWriteVideoAPIErrorStatusAndRedactionContract|TestVideoHandlersMapStorageNotFoundAndValidationErrors|TestVideoConfigAPIReadsAllPresetKindsAndRejectsEscapingCLIOutput|TestVideoBatchAndItemAPIContract|TestVideoAttemptAPIExecutesGetsAndCancels|TestVideoBatchSSESnapshotStateHeartbeatAndCancellation|TestVideoCLIWorkspaceSaveCleanupAndNoClearEndpointContract|TestVideoTailSSESaveAssetSelectionAndCancelContract' -count=1
```

Result:

```text
ok github.com/ekk1/ai-desktop/internal/web 0.131s
```

### Mutation checks

The following temporary mutations were applied one at a time and restored before final verification:

- Reversed Asset-summary ID sorting: `TestVideoBatchAndItemAPIContract` failed with reversed IDs.
- Removed `DisallowUnknownFields`: config, Item, Attempt cancel, CLI save, and Tail tests failed because unknown payloads returned 200/202.
- Changed Batch and Tail heartbeat comments to `: pulse`: their respective SSE contract tests failed on the unexpected line.
- Removed Batch and Tail deferred unsubscribe calls (retaining the variables only for compilation): each SSE test failed with subscriber count 1 instead of 0.

## Final verification

```text
go test -race ./internal/web ./internal/app ./internal/videogen -count=1
ok github.com/ekk1/ai-desktop/internal/web 1.443s
ok github.com/ekk1/ai-desktop/internal/app 1.177s
ok github.com/ekk1/ai-desktop/internal/videogen 5.622s

go test ./... -count=1
all packages passed

go vet ./...
exit 0, no output

git diff --check
exit 0, no output
```

## Files changed

- `internal/web/video_batch.go`
- `internal/web/video_config_test.go`
- `internal/web/video_batch_test.go`
- `internal/web/video_attempt_test.go`
- `internal/web/video_tail_test.go`
- `internal/web/video_error_test.go`
- `.superpowers/sdd/2026-08-31-video-workspace/task-10-fix-4-report.md`

## Self-review

- Confirmed no bulk Item update/delete endpoint was added.
- Confirmed no `internal/videogen` production code was changed.
- Confirmed mutation experiments were fully restored; only the explicit executor-shutdown mapping remains as production code.
- The SSE tests use real domain managers and inspect their private subscriber-map length only after the handler has returned, because unsubscription has no public observable API; this directly detects a missing deferred unsubscribe without introducing production-only test hooks.

## Concerns

None.
