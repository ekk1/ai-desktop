package backend

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/ekk1/ai-desktop/internal/store"
)

const profileSchemaVersion = 2

var (
	ErrNotFound = errors.New("backend profile not found")
	ErrConflict = errors.New("backend profile conflict")
)

type profileDocument struct {
	SchemaVersion int       `json:"schema_version"`
	Profiles      []Profile `json:"profiles"`
}

type Repository struct {
	mu       sync.RWMutex
	path     string
	profiles []Profile
}

func OpenRepository(path string) (*Repository, error) {
	document := profileDocument{}
	err := store.ReadJSON(path, &document)
	if errors.Is(err, os.ErrNotExist) {
		document = profileDocument{SchemaVersion: profileSchemaVersion, Profiles: []Profile{}}
		if err := store.WriteJSON(path, document, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	migrated, err := migrateProfileDocument(&document)
	if err != nil {
		return nil, err
	}
	if migrated {
		if err := store.WriteJSON(path, document, 0o600); err != nil {
			return nil, fmt.Errorf("save migrated backend profiles: %w", err)
		}
	}
	seen := make(map[string]struct{}, len(document.Profiles))
	for _, profile := range document.Profiles {
		if profile.ID == "" {
			return nil, fmt.Errorf("backend profile has an empty ID")
		}
		if _, exists := seen[profile.ID]; exists {
			return nil, fmt.Errorf("duplicate backend profile ID %q", profile.ID)
		}
		seen[profile.ID] = struct{}{}
		if err := profile.Validate(); err != nil {
			return nil, fmt.Errorf("validate backend profile %q: %w", profile.ID, err)
		}
	}
	return &Repository{path: path, profiles: cloneProfiles(document.Profiles)}, nil
}

func (repository *Repository) List() []Profile {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	profiles := cloneProfiles(repository.profiles)
	sort.Slice(profiles, func(left, right int) bool {
		return strings.ToLower(profiles[left].Name) < strings.ToLower(profiles[right].Name)
	})
	return profiles
}

func (repository *Repository) Get(id string) (Profile, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	for _, profile := range repository.profiles {
		if profile.ID == id {
			return cloneProfile(profile), true
		}
	}
	return Profile{}, false
}

func (repository *Repository) Create(profile Profile) (Profile, error) {
	profile = normalizeProfile(profile)
	if err := profile.Validate(); err != nil {
		return Profile{}, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if profile.ID == "" {
		id, err := randomID()
		if err != nil {
			return Profile{}, err
		}
		profile.ID = id
	}
	for _, existing := range repository.profiles {
		if existing.ID == profile.ID {
			return Profile{}, fmt.Errorf("%w: ID %q already exists", ErrConflict, profile.ID)
		}
	}
	updated := append(cloneProfiles(repository.profiles), cloneProfile(profile))
	if err := repository.save(updated); err != nil {
		return Profile{}, err
	}
	repository.profiles = updated
	return cloneProfile(profile), nil
}

func (repository *Repository) Update(id string, profile Profile) (Profile, error) {
	profile.ID = id
	profile = normalizeProfile(profile)
	if err := profile.Validate(); err != nil {
		return Profile{}, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	updated := cloneProfiles(repository.profiles)
	found := false
	for index := range updated {
		if updated[index].ID == id {
			updated[index] = cloneProfile(profile)
			found = true
			break
		}
	}
	if !found {
		return Profile{}, ErrNotFound
	}
	if err := repository.save(updated); err != nil {
		return Profile{}, err
	}
	repository.profiles = updated
	return cloneProfile(profile), nil
}

func migrateProfileDocument(document *profileDocument) (bool, error) {
	switch document.SchemaVersion {
	case 1:
		for index := range document.Profiles {
			document.Profiles[index] = normalizeProfile(document.Profiles[index])
		}
		document.SchemaVersion = profileSchemaVersion
		return true, nil
	case profileSchemaVersion:
		return false, nil
	default:
		return false, fmt.Errorf("backend profile schema version %d is unsupported", document.SchemaVersion)
	}
}

func (repository *Repository) Delete(id string) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	updated := make([]Profile, 0, len(repository.profiles))
	found := false
	for _, profile := range repository.profiles {
		if profile.ID == id {
			found = true
			continue
		}
		updated = append(updated, cloneProfile(profile))
	}
	if !found {
		return ErrNotFound
	}
	if err := repository.save(updated); err != nil {
		return err
	}
	repository.profiles = updated
	return nil
}

func (repository *Repository) save(profiles []Profile) error {
	return store.WriteJSON(repository.path, profileDocument{
		SchemaVersion: profileSchemaVersion,
		Profiles:      profiles,
	}, 0o600)
}

func randomID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate backend profile ID: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func cloneProfiles(profiles []Profile) []Profile {
	clones := make([]Profile, len(profiles))
	for index, profile := range profiles {
		clones[index] = cloneProfile(profile)
	}
	return clones
}
