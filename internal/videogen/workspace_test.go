package videogen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ekk1/ai-desktop/internal/asset"
)

// This fails if a CLI workspace stages anything other than the persisted,
// selected snapshot assets in their declared order, or leaks absolute paths.
func TestWorkspacePreparesOrderedSelectedAssetsAndManifest(t *testing.T) {
	manager, assets := workspaceFixture(t)
	first := importFixtureAsset(t, assets, "video/webm")
	second := importFixtureAsset(t, assets, "image/png")
	snapshot := validWorkspaceSnapshot([]AssetSnapshot{
		{ID: first.ID, SHA256: first.SHA256, MediaType: first.MediaType, DisplayName: first.DisplayName, Size: first.Size, Role: "reference_video", Order: 0},
		{ID: second.ID, SHA256: second.SHA256, MediaType: second.MediaType, DisplayName: second.DisplayName, Size: second.Size, Role: "reference_image", Order: 1},
	})

	workspace, err := manager.Prepare(workspaceAttemptID, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Base(workspace.Inputs[0].Path), "000-reference-video.webm"; got != want {
		t.Fatalf("first staged name = %q, want %q", got, want)
	}
	if got, want := filepath.Base(workspace.Inputs[1].Path), "001-reference-image.png"; got != want {
		t.Fatalf("second staged name = %q, want %q", got, want)
	}
	if !strings.HasPrefix(workspace.OutputPath, workspace.OutputDir+string(filepath.Separator)) {
		t.Fatalf("output path escaped workspace: %q", workspace.OutputPath)
	}
	assertWorkspaceManifest(t, workspace.ManifestPath, workspaceAttemptID, snapshot.InputAssets)
}

// This fails if a cross-device hard-link error does not copy the exact asset
// bytes and record that fallback in the manifest.
func TestWorkspaceCopiesAfterCrossDeviceLinkFailureAndVerifiesHash(t *testing.T) {
	manager, assets := workspaceFixture(t)
	item := importFixtureAsset(t, assets, "video/webm")
	manager.Link = func(string, string) error { return syscall.EXDEV }
	snapshot := validWorkspaceSnapshot([]AssetSnapshot{{ID: item.ID, SHA256: item.SHA256, MediaType: item.MediaType, DisplayName: item.DisplayName, Size: item.Size, Role: "reference_video", Order: 0}})

	workspace, err := manager.Prepare(workspaceAttemptID, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := workspace.Inputs[0].Method; got != "copy" {
		t.Fatalf("staging method = %q, want copy", got)
	}
	if got := fileSHA256(t, workspace.Inputs[0].Path); got != item.SHA256 {
		t.Fatalf("copied hash = %q, want %q", got, item.SHA256)
	}
	assertWorkspaceManifest(t, workspace.ManifestPath, workspaceAttemptID, snapshot.InputAssets)
}

// This fails if an attacker-controlled attempt identifier can influence any
// workspace path.
func TestWorkspaceRejectsUnsafeAttemptIDs(t *testing.T) {
	manager, _ := workspaceFixture(t)
	for _, attemptID := range []string{"", "../" + workspaceAttemptID, strings.Repeat("z", 32), workspaceAttemptID + "/x"} {
		if _, err := manager.Prepare(attemptID, validWorkspaceSnapshot(nil)); err == nil {
			t.Fatalf("Prepare accepted unsafe attempt ID %q", attemptID)
		}
	}
}

// This fails if a snapshot role can alter the staging filename or if a
// negative/duplicate order makes manifest input selection ambiguous.
func TestWorkspaceRejectsUnsafeRolesAndAmbiguousOrders(t *testing.T) {
	manager, assets := workspaceFixture(t)
	first := importFixtureAsset(t, assets, "video/webm")
	second := importFixtureAsset(t, assets, "image/png")
	for _, inputs := range [][]AssetSnapshot{
		{{ID: first.ID, SHA256: first.SHA256, MediaType: first.MediaType, DisplayName: first.DisplayName, Size: first.Size, Role: "../escape", Order: 0}},
		{{ID: first.ID, SHA256: first.SHA256, MediaType: first.MediaType, DisplayName: first.DisplayName, Size: first.Size, Role: "reference_video", Order: -1}},
		{{ID: first.ID, SHA256: first.SHA256, MediaType: first.MediaType, DisplayName: first.DisplayName, Size: first.Size, Role: "reference_video", Order: 0}, {ID: second.ID, SHA256: second.SHA256, MediaType: second.MediaType, DisplayName: second.DisplayName, Size: second.Size, Role: "reference_image", Order: 0}},
	} {
		if _, err := manager.Prepare(workspaceAttemptID, validWorkspaceSnapshot(inputs)); err == nil {
			t.Fatalf("Prepare accepted invalid inputs %#v", inputs)
		}
	}
}

// This fails if preparation trusts stale snapshots, unavailable assets, or
// archived assets that are not retained by the attempt reference.
func TestWorkspaceRejectsMissingArchivedAndHashMismatchedAssets(t *testing.T) {
	manager, assets := workspaceFixture(t)
	item := importFixtureAsset(t, assets, "video/webm")
	missing := AssetSnapshot{ID: strings.Repeat("a", 32), SHA256: strings.Repeat("b", 64), MediaType: "video/webm", DisplayName: "missing.webm", Size: 1, Role: "reference_video", Order: 0}
	if _, err := manager.Prepare(workspaceAttemptID, validWorkspaceSnapshot([]AssetSnapshot{missing})); !errors.Is(err, ErrVideoAssetNotFound) {
		t.Fatalf("missing asset error = %v", err)
	}
	if _, err := assets.SetState(item.ID, asset.StateArchive); err != nil {
		t.Fatal(err)
	}
	archived := AssetSnapshot{ID: item.ID, SHA256: item.SHA256, MediaType: item.MediaType, DisplayName: item.DisplayName, Size: item.Size, Role: "reference_video", Order: 0}
	if _, err := manager.Prepare(workspaceAttemptID, validWorkspaceSnapshot([]AssetSnapshot{archived})); !errors.Is(err, ErrVideoAssetNotActive) {
		t.Fatalf("archived asset error = %v", err)
	}
	if _, err := assets.SetState(item.ID, asset.StateActive); err != nil {
		t.Fatal(err)
	}
	archived.SHA256 = strings.Repeat("c", 64)
	if _, err := manager.Prepare(workspaceAttemptID, validWorkspaceSnapshot([]AssetSnapshot{archived})); err == nil {
		t.Fatal("Prepare accepted a hash-mismatched asset")
	}
}

// This fails if a preset output path can leave outputs/, even when supplied
// through a directly constructed snapshot.
func TestWorkspaceRejectsEscapingOutputPath(t *testing.T) {
	manager, _ := workspaceFixture(t)
	snapshot := validWorkspaceSnapshot(nil)
	snapshot.CLIPreset.OutputRelativePath = "outputs/../../outside.webm"
	if _, err := manager.Prepare(workspaceAttemptID, snapshot); err == nil {
		t.Fatal("Prepare accepted an escaping output path")
	}
}

// This fails if cleanup removes a parent, sibling, or arbitrary supplied path
// instead of exactly the named attempt workspace.
func TestWorkspaceCleanupStaysWithinAttemptBoundary(t *testing.T) {
	manager, _ := workspaceFixture(t)
	workspace, err := manager.Prepare(workspaceAttemptID, validWorkspaceSnapshot(nil))
	if err != nil {
		t.Fatal(err)
	}
	siblingID := "fedcba9876543210fedcba9876543210"
	sibling := filepath.Join(filepath.Dir(workspace.Root), siblingID)
	if err := os.Mkdir(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := manager.Cleanup("../" + workspaceAttemptID); err == nil {
		t.Fatal("Cleanup accepted unsafe attempt ID")
	}
	if err := manager.Cleanup(workspaceAttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace remains after cleanup: %v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("cleanup removed sibling: %v", err)
	}
}

const workspaceAttemptID = "0123456789abcdef0123456789abcdef"

func workspaceFixture(t *testing.T) (*WorkspaceManager, *asset.Repository) {
	t.Helper()
	root := t.TempDir()
	assets, err := asset.OpenRepository(filepath.Join(root, "assets.json"), filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	return NewWorkspaceManager(root, assets), assets
}

func validWorkspaceSnapshot(inputs []AssetSnapshot) Snapshot {
	snapshot := validCLISnapshot()
	snapshot.InputAssets = inputs
	return snapshot
}

func assertWorkspaceManifest(t *testing.T, path, attemptID string, want []AssetSnapshot) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 1 || manifest.AttemptID != attemptID || len(manifest.Inputs) != len(want) {
		t.Fatalf("manifest = %#v", manifest)
	}
	for index, input := range manifest.Inputs {
		if input.AssetID != want[index].ID || input.SHA256 != want[index].SHA256 || filepath.IsAbs(input.Path) || strings.Contains(input.Path, "..") {
			t.Fatalf("manifest input %d = %#v", index, input)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("manifest mode = %o, want 600", got)
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
