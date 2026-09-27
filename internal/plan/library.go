package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Library is the durable, reusable wave-scheme catalogue. It mirrors the
// atomic-commit pattern used by service.MeasurementArchive: a temp file,
// fsync and rename per mutation, and a full reload on startup.
type Library struct {
	mu      sync.Mutex
	path    string
	schemes map[string]Scheme
}

func OpenLibrary(directory string) (*Library, error) {
	library := &Library{schemes: make(map[string]Scheme)}
	if directory == "" {
		return library, nil
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, err
	}
	library.path = filepath.Join(directory, "wave-schemes.json")
	data, err := os.ReadFile(library.path)
	if errors.Is(err, os.ErrNotExist) {
		return library, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &library.schemes); err != nil {
		return nil, fmt.Errorf("read wave scheme library: %w", err)
	}
	if library.schemes == nil {
		return nil, errors.New("wave scheme library must contain a scheme map")
	}
	return library, nil
}

func (l *Library) commit(next map[string]Scheme) error {
	if l.path != "" {
		data, err := json.MarshalIndent(next, "", "  ")
		if err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(l.path), "wave-schemes-*.tmp")
		if err != nil {
			return err
		}
		name := file.Name()
		defer os.Remove(name)
		if _, err = file.Write(data); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Rename(name, l.path); err != nil {
			return err
		}
	}
	l.schemes = next
	return nil
}

func (l *Library) Create(input SchemeInput) (Scheme, error) {
	scheme := input.toScheme(uuid.NewString(), time.Now().UTC())
	if err := scheme.Validate(); err != nil {
		return Scheme{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	next := l.clone()
	next[scheme.ID] = scheme
	if err := l.commit(next); err != nil {
		return Scheme{}, err
	}
	return scheme, nil
}

func (l *Library) Update(id string, input SchemeInput) (Scheme, error) {
	l.mu.Lock()
	existing, exists := l.schemes[id]
	l.mu.Unlock()
	if !exists {
		return Scheme{}, ErrNotFound
	}
	updated := input.toScheme(id, time.Now().UTC())
	updated.CreatedAt = existing.CreatedAt
	if err := updated.Validate(); err != nil {
		return Scheme{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	next := l.clone()
	next[id] = updated
	if err := l.commit(next); err != nil {
		return Scheme{}, err
	}
	return updated, nil
}

func (l *Library) Delete(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.schemes[id]; !exists {
		return false
	}
	next := l.clone()
	delete(next, id)
	_ = l.commit(next)
	return true
}

func (l *Library) Get(id string) (Scheme, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	scheme, exists := l.schemes[id]
	return scheme, exists
}

func (l *Library) List() []Scheme {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := make([]Scheme, 0, len(l.schemes))
	for _, scheme := range l.schemes {
		result = append(result, scheme)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}

func (l *Library) clone() map[string]Scheme {
	next := make(map[string]Scheme, len(l.schemes)+1)
	for id, scheme := range l.schemes {
		scheme.Sections = append([]string(nil), scheme.Sections...)
		next[id] = scheme
	}
	return next
}
