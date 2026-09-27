// Package plan provides a library of reusable wave-making plans and an
// executor that turns a plan into a supervised run of the wave controller.
package plan

import (
    "encoding/json"
    "errors"
    "fmt"
    "math"
    "os"
    "path/filepath"
    "sort"
    "strings"
    "sync"
    "time"

    "github.com/google/uuid"
)

// WavePlan is a reusable wave-making recipe saved by test staff. It records
// the target waveform, its duration, and the tank sections the plan may run
// on.
type WavePlan struct {
    ID         string    `json:"id"`
    Name       string    `json:"name"`
    Frequency  float64   `json:"frequency"`
    Amplitude  float64   `json:"amplitude"`
    DurationMs int64     `json:"duration_ms"`
    Sections   []string  `json:"sections"`
    Revision   string    `json:"revision"`
    CreatedAt  time.Time `json:"created_at"`
    UpdatedAt  time.Time `json:"updated_at"`
}

func (p WavePlan) Duration() time.Duration {
    return time.Duration(p.DurationMs) * time.Millisecond
}

// PlanInput carries the fields an operator supplies when saving a plan. A
// non-empty ID updates the existing plan instead of creating a new one.
type PlanInput struct {
    ID         string   `json:"id"`
    Name       string   `json:"name"`
    Frequency  float64  `json:"frequency"`
    Amplitude  float64  `json:"amplitude"`
    DurationMs int64    `json:"duration_ms"`
    Sections   []string `json:"sections"`
}

func (input PlanInput) validate() error {
    if strings.TrimSpace(input.Name) == "" {
        return errors.New("name is required")
    }
    if math.IsNaN(input.Frequency) || math.IsInf(input.Frequency, 0) || input.Frequency <= 0 {
        return errors.New("frequency must be a positive finite value")
    }
    if math.IsNaN(input.Amplitude) || math.IsInf(input.Amplitude, 0) || input.Amplitude <= 0 {
        return errors.New("amplitude must be a positive finite value")
    }
    if input.DurationMs <= 0 {
        return errors.New("duration_ms must be positive")
    }
    sections := make([]string, 0, len(input.Sections))
    seen := make(map[string]bool, len(input.Sections))
    for _, section := range input.Sections {
        section = strings.TrimSpace(section)
        if section == "" {
            return errors.New("sections must not contain empty names")
        }
        if !seen[section] {
            seen[section] = true
            sections = append(sections, section)
        }
    }
    if len(sections) == 0 {
        return errors.New("at least one applicable section is required")
    }
    return nil
}

func (input PlanInput) normalizedSections() []string {
    sections := make([]string, 0, len(input.Sections))
    seen := make(map[string]bool, len(input.Sections))
    for _, section := range input.Sections {
        section = strings.TrimSpace(section)
        if !seen[section] {
            seen[section] = true
            sections = append(sections, section)
        }
    }
    sort.Strings(sections)
    return sections
}

// Applicable reports whether the plan may run on the given section.
func (p WavePlan) Applicable(section string) bool {
    for _, candidate := range p.Sections {
        if candidate == section {
            return true
        }
    }
    return false
}

// Library stores reusable wave plans, committed to disk before they are
// published in memory, mirroring the measurement archive.
type Library struct {
    mu    sync.Mutex
    path  string
    plans map[string]WavePlan
}

func OpenLibrary(directory string) (*Library, error) {
    library := &Library{plans: make(map[string]WavePlan)}
    if directory == "" {
        return library, nil
    }
    if err := os.MkdirAll(directory, 0o750); err != nil {
        return nil, err
    }
    library.path = filepath.Join(directory, "wave-plans.json")
    data, err := os.ReadFile(library.path)
    if errors.Is(err, os.ErrNotExist) {
        return library, nil
    }
    if err != nil {
        return nil, err
    }
    if err := json.Unmarshal(data, &library.plans); err != nil {
        return nil, fmt.Errorf("read wave plan library: %w", err)
    }
    if library.plans == nil {
        return nil, errors.New("wave plan library must contain a plan map")
    }
    return library, nil
}

func clonePlan(value WavePlan) WavePlan {
    value.Sections = append([]string(nil), value.Sections...)
    return value
}

func (l *Library) commit(next map[string]WavePlan) error {
    if l.path != "" {
        data, err := json.MarshalIndent(next, "", "  ")
        if err != nil {
            return err
        }
        file, err := os.CreateTemp(filepath.Dir(l.path), "wave-plans-*.tmp")
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
    l.plans = next
    return nil
}

func (l *Library) clone() map[string]WavePlan {
    next := make(map[string]WavePlan, len(l.plans))
    for id, plan := range l.plans {
        next[id] = plan
    }
    return next
}

// Save creates a new plan or, when input.ID is set, replaces the stored plan
// while keeping its identity and creation time.
func (l *Library) Save(input PlanInput, at time.Time) (WavePlan, error) {
    if err := input.validate(); err != nil {
        return WavePlan{}, err
    }
    l.mu.Lock()
    defer l.mu.Unlock()
    next := l.clone()
    now := at.UTC()
    plan := WavePlan{
        Name:       strings.TrimSpace(input.Name),
        Frequency:  input.Frequency,
        Amplitude:  input.Amplitude,
        DurationMs: input.DurationMs,
        Sections:   input.normalizedSections(),
        Revision:   uuid.NewString(),
        CreatedAt:  now,
        UpdatedAt:  now,
    }
    if input.ID != "" {
        existing, ok := l.plans[input.ID]
        if !ok {
            return WavePlan{}, errors.New("plan not found")
        }
        plan.ID = existing.ID
        plan.CreatedAt = existing.CreatedAt
    } else {
        plan.ID = uuid.NewString()
    }
    next[plan.ID] = plan
    if err := l.commit(next); err != nil {
        return WavePlan{}, err
    }
    return clonePlan(plan), nil
}

func (l *Library) Get(id string) (WavePlan, bool) {
    l.mu.Lock()
    defer l.mu.Unlock()
    plan, exists := l.plans[id]
    return clonePlan(plan), exists
}

func (l *Library) List() []WavePlan {
    l.mu.Lock()
    defer l.mu.Unlock()
    result := make([]WavePlan, 0, len(l.plans))
    for _, plan := range l.plans {
        result = append(result, clonePlan(plan))
    }
    sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
    return result
}

func (l *Library) Delete(id string) error {
    l.mu.Lock()
    defer l.mu.Unlock()
    if _, exists := l.plans[id]; !exists {
        return errors.New("plan not found")
    }
    next := l.clone()
    delete(next, id)
    return l.commit(next)
}
