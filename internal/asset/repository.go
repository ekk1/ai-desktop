package asset

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ekk1/ai-desktop/internal/store"
)

const assetSchemaVersion = 1

var (
	ErrNotFound   = errors.New("asset not found")
	ErrReferenced = errors.New("asset is referenced")
)

type document struct {
	SchemaVersion int     `json:"schema_version"`
	Assets        []Asset `json:"assets"`
}

type Repository struct {
	mu        sync.RWMutex
	indexPath string
	filesDir  string
	assets    []Asset
}

func OpenRepository(indexPath, filesDir string) (*Repository, error) {
	if err := os.MkdirAll(filesDir, 0o700); err != nil {
		return nil, fmt.Errorf("create asset files directory: %w", err)
	}
	stored := document{}
	err := store.ReadJSONWithBackup(indexPath, &stored, 0o600, func() error {
		return validateDocument(stored)
	})
	if errors.Is(err, os.ErrNotExist) {
		stored = document{SchemaVersion: assetSchemaVersion, Assets: []Asset{}}
		if err := store.WriteJSON(indexPath, stored, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if err := validateDocument(stored); err != nil {
		return nil, err
	}
	return &Repository{indexPath: indexPath, filesDir: filesDir, assets: cloneAssets(stored.Assets)}, nil
}

func (repository *Repository) Import(input ImportInput) (Asset, error) {
	if input.Reader == nil {
		return Asset{}, fmt.Errorf("asset reader is required")
	}
	if strings.TrimSpace(input.DisplayName) == "" {
		input.DisplayName = "asset"
	}
	if input.MediaType == "" {
		input.MediaType = "application/octet-stream"
	}
	temporary, err := os.CreateTemp(repository.filesDir, ".import-*")
	if err != nil {
		return Asset{}, fmt.Errorf("create asset import file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(temporary, hash), input.Reader)
	if err != nil {
		_ = temporary.Close()
		return Asset{}, fmt.Errorf("copy asset content: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return Asset{}, fmt.Errorf("sync asset content: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return Asset{}, fmt.Errorf("close asset content: %w", err)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	storedName := digest + safeExtension(input.MediaType, input.DisplayName)
	destination := filepath.Join(repository.filesDir, storedName)
	newPhysicalFile := false
	if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(temporaryPath, destination); err != nil {
			return Asset{}, fmt.Errorf("store asset content: %w", err)
		}
		newPhysicalFile = true
	} else if err != nil {
		return Asset{}, fmt.Errorf("inspect asset content: %w", err)
	}

	width, height := imageDimensions(destination, input.MediaType)
	id, err := randomID()
	if err != nil {
		if newPhysicalFile {
			_ = os.Remove(destination)
		}
		return Asset{}, err
	}
	now := time.Now().UTC()
	created := Asset{
		ID: id, SHA256: digest, MediaType: input.MediaType, DisplayName: filepath.Base(input.DisplayName),
		StoredName: storedName, Size: size, Width: width, Height: height, State: StateArchive,
		Source: input.Source, CreatedAt: now, UpdatedAt: now,
	}

	repository.mu.Lock()
	defer repository.mu.Unlock()
	updated := append(cloneAssets(repository.assets), created)
	if err := repository.save(updated); err != nil {
		if newPhysicalFile {
			_ = os.Remove(destination)
		}
		return Asset{}, err
	}
	repository.assets = updated
	return cloneAsset(created), nil
}

func (repository *Repository) List(filter Filter) []Asset {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]Asset, 0)
	for _, item := range repository.assets {
		if matchesFilter(item, filter) {
			result = append(result, cloneAsset(item))
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].CreatedAt.After(result[right].CreatedAt) })
	return result
}

func (repository *Repository) Get(id string) (Asset, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	for _, item := range repository.assets {
		if item.ID == id {
			return cloneAsset(item), true
		}
	}
	return Asset{}, false
}

func (repository *Repository) SetState(id string, state State) (Asset, error) {
	if err := validateState(state); err != nil {
		return Asset{}, err
	}
	return repository.update(id, func(item *Asset) { item.State = state })
}

func (repository *Repository) SetStates(ids []string, state State) ([]Asset, error) {
	if err := validateState(state); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("at least one asset ID is required")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()

	indexes := make([]int, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		index := -1
		for candidate := range repository.assets {
			if repository.assets[candidate].ID == id {
				index = candidate
				break
			}
		}
		if index < 0 {
			return nil, ErrNotFound
		}
		indexes = append(indexes, index)
	}

	now := time.Now().UTC()
	updated := cloneAssets(repository.assets)
	result := make([]Asset, 0, len(indexes))
	for _, index := range indexes {
		updated[index].State = state
		updated[index].UpdatedAt = now
		result = append(result, cloneAsset(updated[index]))
	}
	if err := repository.save(updated); err != nil {
		return nil, err
	}
	repository.assets = updated
	return result, nil
}

func (repository *Repository) UpdateMetadata(id, displayName, notes string) (Asset, error) {
	if strings.TrimSpace(displayName) == "" {
		return Asset{}, fmt.Errorf("display name is required")
	}
	return repository.update(id, func(item *Asset) {
		item.DisplayName = filepath.Base(displayName)
		item.Notes = notes
	})
}

func (repository *Repository) AddReference(id string, reference Reference) (Asset, error) {
	if reference.Module == "" || reference.RecordID == "" {
		return Asset{}, fmt.Errorf("reference module and record_id are required")
	}
	return repository.update(id, func(item *Asset) {
		for _, existing := range item.References {
			if existing == reference {
				return
			}
		}
		item.References = append(item.References, reference)
	})
}

func (repository *Repository) RemoveReference(id string, reference Reference) (Asset, error) {
	return repository.update(id, func(item *Asset) {
		kept := item.References[:0]
		for _, existing := range item.References {
			if existing != reference {
				kept = append(kept, existing)
			}
		}
		item.References = kept
	})
}

func (repository *Repository) Delete(id string) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	index := -1
	for candidate := range repository.assets {
		if repository.assets[candidate].ID == id {
			index = candidate
			break
		}
	}
	if index < 0 {
		return ErrNotFound
	}
	item := repository.assets[index]
	if len(item.References) > 0 {
		return fmt.Errorf("%w: %d references", ErrReferenced, len(item.References))
	}
	updated := append(cloneAssets(repository.assets[:index]), cloneAssets(repository.assets[index+1:])...)
	if err := repository.save(updated); err != nil {
		return err
	}
	repository.assets = updated
	for _, remaining := range updated {
		if remaining.SHA256 == item.SHA256 {
			return nil
		}
	}
	if err := os.Remove(filepath.Join(repository.filesDir, item.StoredName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove asset content: %w", err)
	}
	return nil
}

func (repository *Repository) OpenContent(id string) (*os.File, Asset, error) {
	item, ok := repository.Get(id)
	if !ok {
		return nil, Asset{}, ErrNotFound
	}
	file, err := os.Open(filepath.Join(repository.filesDir, item.StoredName))
	if err != nil {
		return nil, Asset{}, fmt.Errorf("open asset content: %w", err)
	}
	return file, item, nil
}

func (repository *Repository) update(id string, change func(*Asset)) (Asset, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	updated := cloneAssets(repository.assets)
	for index := range updated {
		if updated[index].ID == id {
			change(&updated[index])
			updated[index].UpdatedAt = time.Now().UTC()
			if err := repository.save(updated); err != nil {
				return Asset{}, err
			}
			repository.assets = updated
			return cloneAsset(updated[index]), nil
		}
	}
	return Asset{}, ErrNotFound
}

func (repository *Repository) save(assets []Asset) error {
	stored := document{SchemaVersion: assetSchemaVersion, Assets: assets}
	if err := validateDocument(stored); err != nil {
		return err
	}
	return store.WriteJSON(repository.indexPath, stored, 0o600)
}

func safeExtension(mediaType, displayName string) string {
	known := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp", "video/mp4": ".mp4", "video/webm": ".webm", "text/plain": ".txt"}
	if extension := known[mediaType]; extension != "" {
		return extension
	}
	if extensions, _ := mime.ExtensionsByType(mediaType); len(extensions) > 0 && len(extensions[0]) <= 8 {
		return extensions[0]
	}
	extension := strings.ToLower(filepath.Ext(filepath.Base(displayName)))
	if len(extension) >= 2 && len(extension) <= 8 {
		for _, character := range extension[1:] {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return ".bin"
			}
		}
		return extension
	}
	return ".bin"
}

func imageDimensions(path, mediaType string) (int, int) {
	if !strings.HasPrefix(mediaType, "image/") {
		return 0, 0
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer file.Close()
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return 0, 0
	}
	return config.Width, config.Height
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate asset ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func validateDocument(stored document) error {
	if stored.SchemaVersion != assetSchemaVersion {
		return fmt.Errorf("asset schema version %d is unsupported", stored.SchemaVersion)
	}
	ids := make(map[string]struct{}, len(stored.Assets))
	for _, item := range stored.Assets {
		if !validHex(item.ID, 32) {
			return fmt.Errorf("asset ID %q is invalid", item.ID)
		}
		if _, duplicate := ids[item.ID]; duplicate {
			return fmt.Errorf("duplicate asset ID %q", item.ID)
		}
		ids[item.ID] = struct{}{}
		if !validHex(item.SHA256, 64) {
			return fmt.Errorf("asset %q has invalid SHA-256", item.ID)
		}
		if _, _, err := mime.ParseMediaType(item.MediaType); err != nil {
			return fmt.Errorf("asset %q has invalid media type: %w", item.ID, err)
		}
		if strings.TrimSpace(item.DisplayName) == "" || filepath.Base(item.DisplayName) != item.DisplayName || item.DisplayName == "." || item.DisplayName == ".." {
			return fmt.Errorf("asset %q has invalid display name", item.ID)
		}
		if item.StoredName == "" || filepath.Base(item.StoredName) != item.StoredName || item.StoredName == "." || item.StoredName == ".." || !strings.HasPrefix(item.StoredName, item.SHA256) {
			return fmt.Errorf("asset %q has invalid stored name", item.ID)
		}
		if item.Size < 0 || item.Width < 0 || item.Height < 0 {
			return fmt.Errorf("asset %q has invalid dimensions or size", item.ID)
		}
		if err := validateState(item.State); err != nil {
			return fmt.Errorf("asset %q: %w", item.ID, err)
		}
		if item.CreatedAt.IsZero() || item.UpdatedAt.Before(item.CreatedAt) {
			return fmt.Errorf("asset %q has invalid timestamps", item.ID)
		}
		references := make(map[Reference]struct{}, len(item.References))
		for _, reference := range item.References {
			if reference.Module == "" || reference.RecordID == "" {
				return fmt.Errorf("asset %q has an invalid reference", item.ID)
			}
			if _, duplicate := references[reference]; duplicate {
				return fmt.Errorf("asset %q has a duplicate reference", item.ID)
			}
			references[reference] = struct{}{}
		}
	}
	return nil
}

func validHex(value string, length int) bool {
	if len(value) != length || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func cloneAssets(items []Asset) []Asset {
	clones := make([]Asset, len(items))
	for index, item := range items {
		clones[index] = cloneAsset(item)
	}
	return clones
}
