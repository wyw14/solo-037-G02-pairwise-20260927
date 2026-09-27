package plan

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/wyw14/solo-037/internal/wave"
)

const (
	MinDuration    = 100 * time.Millisecond
	MaxDuration    = time.Hour
	MaxFrequencyHz = 10.0
)

// Millis is a time.Duration that serialises to whole milliseconds so scheme
// files and HTTP payloads do not leak Go's nanosecond duration encoding.
type Millis time.Duration

func (m Millis) Duration() time.Duration { return time.Duration(m) }

func (m Millis) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(m).Milliseconds())
}

func (m *Millis) UnmarshalJSON(data []byte) error {
	var milliseconds int64
	if err := json.Unmarshal(data, &milliseconds); err != nil {
		return err
	}
	if milliseconds < 0 {
		return errors.New("duration_ms must not be negative")
	}
	*m = Millis(time.Duration(milliseconds) * time.Millisecond)
	return nil
}

// Scheme is a reusable wave-making recipe: target waveform, duration and the
// tank sections it is allowed to run in (empty sections means every section).
type Scheme struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	FrequencyHz float64   `json:"frequency_hz"`
	Amplitude   float64   `json:"amplitude"`
	Duration    Millis    `json:"duration_ms"`
	Sections    []string  `json:"sections"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type SchemeInput struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	FrequencyHz float64  `json:"frequency_hz"`
	Amplitude   float64  `json:"amplitude"`
	DurationMS  int64    `json:"duration_ms"`
	Sections    []string `json:"sections"`
}

func (input SchemeInput) toScheme(id string, at time.Time) Scheme {
	sections := make([]string, 0, len(input.Sections))
	for _, section := range input.Sections {
		if trimmed := strings.TrimSpace(section); trimmed != "" {
			sections = append(sections, trimmed)
		}
	}
	return Scheme{
		ID:          id,
		Name:        strings.TrimSpace(input.Name),
		Description: strings.TrimSpace(input.Description),
		FrequencyHz: input.FrequencyHz,
		Amplitude:   input.Amplitude,
		Duration:    Millis(time.Duration(input.DurationMS) * time.Millisecond),
		Sections:    sections,
		CreatedAt:   at.UTC(),
		UpdatedAt:   at.UTC(),
	}
}

// Validate verifies the recipe itself; section membership is checked against
// the live tank registry during preflight.
func (s Scheme) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("scheme name is required")
	}
	if math.IsNaN(s.FrequencyHz) || math.IsInf(s.FrequencyHz, 0) || s.FrequencyHz <= 0 || s.FrequencyHz > MaxFrequencyHz {
		return errors.New("frequency_hz must be finite, positive and at most 10")
	}
	if math.IsNaN(s.Amplitude) || math.IsInf(s.Amplitude, 0) || s.Amplitude <= 0 {
		return errors.New("amplitude must be finite and positive")
	}
	duration := s.Duration.Duration()
	if duration < MinDuration || duration > MaxDuration {
		return errors.New("duration_ms must be between 100ms and 1h")
	}
	for _, section := range s.Sections {
		if strings.TrimSpace(section) == "" {
			return errors.New("sections must not contain blank values")
		}
	}
	return nil
}

func (s Scheme) Profile() wave.Profile {
	return wave.NewProfile(s.FrequencyHz, s.Amplitude, s.Duration.Duration())
}

// AppliesTo reports whether the scheme may be used on the given section.
// A scheme with no section restriction applies to every section.
func (s Scheme) AppliesTo(section string) bool {
	if len(s.Sections) == 0 {
		return true
	}
	for _, candidate := range s.Sections {
		if candidate == section {
			return true
		}
	}
	return false
}
