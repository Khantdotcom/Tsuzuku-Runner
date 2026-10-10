// Package workload validates workload submissions and normalizes them into
// the Spec that is stored with every job.
package workload

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Defaults applied when a request leaves a field out. A default above the
// configured maximum is lowered to that maximum.
const (
	DefaultImage          = "golang:1.27"
	DefaultCPUMillis      = 1000
	DefaultMemoryMB       = 1024
	DefaultTimeoutSeconds = 600
)

const (
	minCPUMillis         = 100
	minMemoryMB          = 64
	maxRepositoryLen     = 2048
	maxRevisionLen       = 255
	maxCommandLen        = 4096
	maxCriteria          = 20
	maxCriterionLen      = 500
	maxIdempotencyKeyLen = 255
)

var (
	// Official images only, any tag. Milestone 12 revisits image policy.
	imagePattern    = regexp.MustCompile(`^(golang|node|python)(:[A-Za-z0-9_][A-Za-z0-9._-]{0,127})?$`)
	revisionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@^~+-]*$`)
)

// Limits are the policy maximums a workload may request.
type Limits struct {
	MaxCPUMillis int
	MaxMemoryMB  int
	MaxTimeout   time.Duration
}

// DefaultLimits returns the limits used when none are configured.
func DefaultLimits() Limits {
	return Limits{MaxCPUMillis: 4000, MaxMemoryMB: 8192, MaxTimeout: time.Hour}
}

// Request is a workload submission as clients send it. Pointer fields
// distinguish "not set" (use the default) from an explicit value.
type Request struct {
	Repository         string               `json:"repository"`
	Revision           string               `json:"revision"`
	Command            string               `json:"command"`
	AcceptanceCriteria []string             `json:"acceptance_criteria"`
	Resources          *ResourcesRequest    `json:"resources"`
	TimeoutSeconds     *int                 `json:"timeout_seconds"`
	Runtime            *RuntimeRequest      `json:"runtime"`
	Verification       *VerificationRequest `json:"verification"`
}

// ResourcesRequest is the requested CPU (in cores) and memory.
type ResourcesRequest struct {
	CPU      *float64 `json:"cpu"`
	MemoryMB *int     `json:"memory_mb"`
}

// RuntimeRequest selects the container image and network access.
type RuntimeRequest struct {
	Image   string `json:"image"`
	Network *bool  `json:"network"`
}

// VerificationRequest is an optional command run after the workload command.
type VerificationRequest struct {
	Command string `json:"command"`
}

// Spec is a validated workload with every default filled in. It is stored as
// workloads.spec and compared field by field for idempotent replays.
type Spec struct {
	Repository         string        `json:"repository"`
	Revision           string        `json:"revision"`
	Command            string        `json:"command"`
	AcceptanceCriteria []string      `json:"acceptance_criteria"`
	Resources          Resources     `json:"resources"`
	TimeoutSeconds     int           `json:"timeout_seconds"`
	Runtime            Runtime       `json:"runtime"`
	Verification       *Verification `json:"verification,omitempty"`
}

// Resources is the CPU (in cores) and memory a workload runs with.
type Resources struct {
	CPU      float64 `json:"cpu"`
	MemoryMB int     `json:"memory_mb"`
}

// Runtime is the container image and whether the container has network access.
type Runtime struct {
	Image   string `json:"image"`
	Network bool   `json:"network"`
}

// Verification is the command that checks the workload's result.
type Verification struct {
	Command string `json:"command"`
}

// CPUMillis returns the CPU request in millicores.
func (s Spec) CPUMillis() int {
	return int(math.Round(s.Resources.CPU * 1000))
}

// VerificationCommand returns the verification command, or nil if there is none.
func (s Spec) VerificationCommand() *string {
	if s.Verification == nil {
		return nil
	}
	cmd := s.Verification.Command
	return &cmd
}

// Normalize validates r against limits and returns the Spec with defaults
// applied. Every error it returns describes a problem with the request.
func (r Request) Normalize(limits Limits) (Spec, error) {
	spec := Spec{
		Repository:         r.Repository,
		Revision:           r.Revision,
		Command:            r.Command,
		AcceptanceCriteria: []string{},
		Resources: Resources{
			CPU:      float64(min(DefaultCPUMillis, limits.MaxCPUMillis)) / 1000,
			MemoryMB: min(DefaultMemoryMB, limits.MaxMemoryMB),
		},
		TimeoutSeconds: min(DefaultTimeoutSeconds, int(limits.MaxTimeout/time.Second)),
		Runtime:        Runtime{Image: DefaultImage, Network: true},
	}

	if err := validateRepository(r.Repository); err != nil {
		return Spec{}, err
	}
	if err := validateRevision(r.Revision); err != nil {
		return Spec{}, err
	}
	if err := validateCommand("command", r.Command); err != nil {
		return Spec{}, err
	}

	if len(r.AcceptanceCriteria) > maxCriteria {
		return Spec{}, fmt.Errorf("acceptance_criteria must have at most %d items", maxCriteria)
	}
	for i, c := range r.AcceptanceCriteria {
		if strings.TrimSpace(c) == "" || utf8.RuneCountInString(c) > maxCriterionLen || strings.ContainsRune(c, 0) {
			return Spec{}, fmt.Errorf("acceptance_criteria[%d] must be 1-%d characters", i, maxCriterionLen)
		}
		spec.AcceptanceCriteria = append(spec.AcceptanceCriteria, c)
	}

	if res := r.Resources; res != nil {
		if res.CPU != nil {
			if err := validateCPU(*res.CPU, limits.MaxCPUMillis); err != nil {
				return Spec{}, err
			}
			spec.Resources.CPU = *res.CPU
		}
		if res.MemoryMB != nil {
			if m := *res.MemoryMB; m < minMemoryMB || m > limits.MaxMemoryMB {
				return Spec{}, fmt.Errorf("resources.memory_mb must be between %d and %d", minMemoryMB, limits.MaxMemoryMB)
			}
			spec.Resources.MemoryMB = *res.MemoryMB
		}
	}

	if r.TimeoutSeconds != nil {
		maxSeconds := int(limits.MaxTimeout / time.Second)
		if t := *r.TimeoutSeconds; t < 1 || t > maxSeconds {
			return Spec{}, fmt.Errorf("timeout_seconds must be between 1 and %d", maxSeconds)
		}
		spec.TimeoutSeconds = *r.TimeoutSeconds
	}

	if rt := r.Runtime; rt != nil {
		if rt.Image != "" {
			image, err := normalizeImage(rt.Image)
			if err != nil {
				return Spec{}, err
			}
			spec.Runtime.Image = image
		}
		if rt.Network != nil {
			spec.Runtime.Network = *rt.Network
		}
	}

	if v := r.Verification; v != nil {
		if err := validateCommand("verification.command", v.Command); err != nil {
			return Spec{}, err
		}
		spec.Verification = &Verification{Command: v.Command}
	}

	return spec, nil
}

// ValidateIdempotencyKey reports whether key is usable as an Idempotency-Key:
// 1-255 visible ASCII characters.
func ValidateIdempotencyKey(key string) error {
	if key == "" || len(key) > maxIdempotencyKeyLen {
		return fmt.Errorf("the Idempotency-Key header must be 1-%d characters", maxIdempotencyKeyLen)
	}
	for i := range len(key) {
		if c := key[i]; c < '!' || c > '~' {
			return errors.New("the Idempotency-Key header must contain only visible ASCII characters")
		}
	}
	return nil
}

func validateRepository(repo string) error {
	const msg = "repository must be a public https:// git URL without credentials, query, or fragment"
	if repo == "" || len(repo) > maxRepositoryLen {
		return fmt.Errorf("repository must be 1-%d characters", maxRepositoryLen)
	}
	u, err := url.Parse(repo)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(repo, " \t\r\n") {
		return errors.New(msg)
	}
	return nil
}

func validateRevision(rev string) error {
	if rev == "" || len(rev) > maxRevisionLen || !revisionPattern.MatchString(rev) {
		return fmt.Errorf("revision must be a branch, tag, or commit of 1-%d characters, starting with a letter or digit", maxRevisionLen)
	}
	return nil
}

func validateCommand(field, cmd string) error {
	if strings.TrimSpace(cmd) == "" || utf8.RuneCountInString(cmd) > maxCommandLen || strings.ContainsRune(cmd, 0) {
		return fmt.Errorf("%s must be 1-%d characters and not blank", field, maxCommandLen)
	}
	return nil
}

func validateCPU(cpu float64, maxMillis int) error {
	millis := cpu * 1000
	if math.IsNaN(cpu) || millis < minCPUMillis || millis > float64(maxMillis) {
		return fmt.Errorf("resources.cpu must be between %g and %g cores", float64(minCPUMillis)/1000, float64(maxMillis)/1000)
	}
	if math.Abs(millis-math.Round(millis)) > 1e-6 {
		return errors.New("resources.cpu must be a multiple of 0.001 cores")
	}
	return nil
}

// normalizeImage checks image against the allowlist and makes the tag explicit.
func normalizeImage(image string) (string, error) {
	if !imagePattern.MatchString(image) {
		return "", errors.New("runtime.image must be an official golang, node, or python image, for example golang:1.27")
	}
	if !strings.Contains(image, ":") {
		image += ":latest"
	}
	return image, nil
}
