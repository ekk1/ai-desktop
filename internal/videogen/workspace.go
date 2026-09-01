package videogen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/videoconfig"
)

const workspaceManifestSchemaVersion = 1

// Workspace is the fixed directory layout made available to a local CLI.
type Workspace struct {
	Root         string
	InputDir     string
	OutputDir    string
	ManifestPath string
	OutputPath   string
	Inputs       []StagedInput
}

// StagedInput records one selected asset's controlled local filename.
type StagedInput struct {
	AssetID string
	SHA256  string
	Role    string
	Order   int
	Path    string
	Method  string
}

// Manifest is the reproducibility record kept inside each CLI workspace.
type Manifest struct {
	SchemaVersion int             `json:"schema_version"`
	AttemptID     string          `json:"attempt_id"`
	Inputs        []ManifestInput `json:"inputs"`
}

type ManifestInput struct {
	AssetID   string `json:"asset_id"`
	SHA256    string `json:"sha256"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	Role      string `json:"role"`
	Order     int    `json:"order"`
	Path      string `json:"path"`
	Method    string `json:"method"`
}

// WorkspaceManager owns the fixed local workspace root.
type WorkspaceManager struct {
	root   string
	assets *asset.Repository
}

func NewWorkspaceManager(root string, assets *asset.Repository) *WorkspaceManager {
	return &WorkspaceManager{root: root, assets: assets}
}

func (manager *WorkspaceManager) Prepare(attemptID string, snapshot Snapshot) (workspace Workspace, err error) {
	if manager == nil || manager.assets == nil || strings.TrimSpace(manager.root) == "" {
		return Workspace{}, fmt.Errorf("video workspace manager requires an asset repository and root")
	}
	if !validWorkspaceAttemptID(attemptID) {
		return Workspace{}, fmt.Errorf("video workspace attempt ID is invalid")
	}
	if snapshot.ExecutionKind != videoconfig.ExecutionLocalCLI || snapshot.CLIPreset == nil || snapshot.HTTPProvider != nil {
		return Workspace{}, fmt.Errorf("video workspace requires a CLI snapshot")
	}
	outputRelativePath, err := workspaceOutputRelativePath(snapshot.CLIPreset.OutputRelativePath)
	if err != nil {
		return Workspace{}, err
	}
	inputs, err := workspaceInputs(snapshot.InputAssets)
	if err != nil {
		return Workspace{}, err
	}

	base := filepath.Clean(manager.root)
	root := filepath.Join(base, attemptID)
	if !workspaceAttemptPath(base, root, attemptID) {
		return Workspace{}, fmt.Errorf("video workspace path is outside its root")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return Workspace{}, fmt.Errorf("create video workspace root: %w", err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		return Workspace{}, fmt.Errorf("protect video workspace root: %w", err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return Workspace{}, fmt.Errorf("create attempt workspace: %w", err)
	}
	completed := false
	defer func() {
		if !completed {
			_ = os.RemoveAll(root)
		}
	}()
	inputDir, outputDir := filepath.Join(root, "inputs"), filepath.Join(root, "outputs")
	if err := os.Mkdir(inputDir, 0o700); err != nil {
		return Workspace{}, fmt.Errorf("create workspace inputs: %w", err)
	}
	if err := os.Mkdir(outputDir, 0o700); err != nil {
		return Workspace{}, fmt.Errorf("create workspace outputs: %w", err)
	}
	workspace = Workspace{Root: root, InputDir: inputDir, OutputDir: outputDir, ManifestPath: filepath.Join(root, "manifest.json"), OutputPath: filepath.Join(outputDir, outputRelativePath), Inputs: make([]StagedInput, 0, len(inputs))}
	manifest := Manifest{SchemaVersion: workspaceManifestSchemaVersion, AttemptID: attemptID, Inputs: make([]ManifestInput, 0, len(inputs))}
	for _, input := range inputs {
		staged, err := manager.stageInput(inputDir, attemptID, input)
		if err != nil {
			return Workspace{}, err
		}
		workspace.Inputs = append(workspace.Inputs, staged)
		relativePath, err := filepath.Rel(root, staged.Path)
		if err != nil || filepath.IsAbs(relativePath) || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
			return Workspace{}, fmt.Errorf("staged input escaped workspace")
		}
		manifest.Inputs = append(manifest.Inputs, ManifestInput{AssetID: staged.AssetID, SHA256: staged.SHA256, MediaType: input.MediaType, Size: input.Size, Role: staged.Role, Order: staged.Order, Path: relativePath, Method: staged.Method})
	}
	if err := writeWorkspaceManifest(workspace.ManifestPath, manifest); err != nil {
		return Workspace{}, fmt.Errorf("write video workspace manifest: %w", err)
	}
	completed = true
	return workspace, nil
}

func (manager *WorkspaceManager) Cleanup(attemptID string) error {
	if manager == nil || strings.TrimSpace(manager.root) == "" || !validWorkspaceAttemptID(attemptID) {
		return fmt.Errorf("video workspace attempt ID is invalid")
	}
	base := filepath.Clean(manager.root)
	root := filepath.Join(base, attemptID)
	if !workspaceAttemptPath(base, root, attemptID) {
		return fmt.Errorf("video workspace cleanup path is outside its root")
	}
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("remove video workspace: %w", err)
	}
	return nil
}

func workspaceInputs(source []AssetSnapshot) ([]AssetSnapshot, error) {
	inputs := append([]AssetSnapshot(nil), source...)
	orders := make(map[int]struct{}, len(inputs))
	for index, input := range inputs {
		if !validGeneratedID(input.ID) || len(input.SHA256) != 64 {
			return nil, fmt.Errorf("workspace input %d identity is invalid", index)
		}
		if _, err := hex.DecodeString(input.SHA256); err != nil || !validVideoRole(input.Role) || input.Order < 0 || input.Size < 0 {
			return nil, fmt.Errorf("workspace input %d metadata is invalid", index)
		}
		if _, duplicate := orders[input.Order]; duplicate {
			return nil, fmt.Errorf("workspace input order %d is duplicated", input.Order)
		}
		orders[input.Order] = struct{}{}
	}
	sort.Slice(inputs, func(left, right int) bool { return inputs[left].Order < inputs[right].Order })
	return inputs, nil
}

func validWorkspaceAttemptID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, character := range id {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func writeWorkspaceManifest(path string, manifest Manifest) (err error) {
	contents, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode video workspace manifest: %w", err)
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".manifest-*")
	if err != nil {
		return fmt.Errorf("create video workspace manifest: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect video workspace manifest: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		return fmt.Errorf("write video workspace manifest: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync video workspace manifest: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close video workspace manifest: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace video workspace manifest: %w", err)
	}
	return nil
}

func (manager *WorkspaceManager) stageInput(inputDir, attemptID string, snapshot AssetSnapshot) (StagedInput, error) {
	source, current, err := manager.assets.OpenContent(snapshot.ID)
	if errors.Is(err, asset.ErrNotFound) {
		return StagedInput{}, fmt.Errorf("%w: %s", ErrVideoAssetNotFound, snapshot.ID)
	}
	if err != nil {
		return StagedInput{}, fmt.Errorf("open video workspace input %q: %w", snapshot.ID, err)
	}
	defer source.Close()
	if current.SHA256 != snapshot.SHA256 || current.MediaType != snapshot.MediaType || current.Size != snapshot.Size || current.DisplayName != snapshot.DisplayName {
		return StagedInput{}, fmt.Errorf("video workspace input %q no longer matches its snapshot", snapshot.ID)
	}
	if current.State != asset.StateActive && !containsReference(current, asset.Reference{Module: videoAttemptReferenceModule, RecordID: attemptID}) {
		return StagedInput{}, fmt.Errorf("%w: %s", ErrVideoAssetNotActive, snapshot.ID)
	}
	if err := verifyWorkspaceHash(source, snapshot.SHA256); err != nil {
		return StagedInput{}, fmt.Errorf("verify video workspace input %q: %w", snapshot.ID, err)
	}
	filename := fmt.Sprintf("%03d-%s%s", snapshot.Order, strings.ReplaceAll(snapshot.Role, "_", "-"), workspaceExtension(snapshot.MediaType))
	destination := filepath.Join(inputDir, filename)
	if filepath.Dir(destination) != inputDir {
		return StagedInput{}, fmt.Errorf("video workspace staging path escaped inputs")
	}
	if err := copyWorkspaceInput(source, destination, snapshot.SHA256); err != nil {
		return StagedInput{}, fmt.Errorf("copy video workspace input %q: %w", snapshot.ID, err)
	}
	return StagedInput{AssetID: snapshot.ID, SHA256: snapshot.SHA256, Role: snapshot.Role, Order: snapshot.Order, Path: destination, Method: "copy"}, nil
}

func copyWorkspaceInput(source *os.File, destination, wantSHA256 string) error {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind source: %w", err)
	}
	target, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}
	_, copyErr := io.Copy(target, source)
	if copyErr == nil {
		copyErr = target.Sync()
	}
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	if err := verifyWorkspacePathHash(destination, wantSHA256); err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("verify copied bytes: %w", err)
	}
	return nil
}

func workspaceAttemptPath(base, root, attemptID string) bool {
	relative, err := filepath.Rel(base, root)
	return err == nil && relative == attemptID && filepath.Dir(root) == base
}

func workspaceOutputRelativePath(value string) (string, error) {
	clean := filepath.Clean(value)
	relative, err := filepath.Rel("outputs", clean)
	if value == "" || clean != value || filepath.IsAbs(value) || err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("CLI output path must remain under outputs/")
	}
	return relative, nil
}

func workspaceExtension(mediaType string) string {
	switch strings.ToLower(mediaType) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "audio/mpeg":
		return ".mp3"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	default:
		return ".bin"
	}
}

func verifyWorkspaceHash(reader io.Reader, want string) error {
	digest := sha256.New()
	if _, err := io.Copy(digest, reader); err != nil {
		return err
	}
	if got := hex.EncodeToString(digest.Sum(nil)); got != want {
		return fmt.Errorf("SHA-256 = %q, want %q", got, want)
	}
	return nil
}

func verifyWorkspacePathHash(path, want string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return verifyWorkspaceHash(file, want)
}
