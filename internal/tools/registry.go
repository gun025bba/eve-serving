package tools

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Manifest is the declarative description of one tool (docs/tool-platform.md §1.1).
// Manifests live in the repository and are embedded at build time; there is no
// runtime registration API.
type Manifest struct {
	Name           string   `yaml:"name"`
	Version        string   `yaml:"version"`
	Description    string   `yaml:"description"`
	Scopes         []string `yaml:"scopes"`
	SideEffect     string   `yaml:"side_effect"`
	Verbs          []string `yaml:"verbs"` // allowed confirmation-card verbs (Q19), required, static; one per call
	TaintOutput    bool     `yaml:"taint_output"`
	Egress         []string `yaml:"egress"`
	Secrets        []string `yaml:"secrets"`
	Timeout        Duration `yaml:"timeout"`
	MaxResultChars int      `yaml:"max_result_chars"`
	Exposure       struct {
		Modes      []string `yaml:"modes"`
		MaxPerTurn int      `yaml:"max_per_turn"`
	} `yaml:"exposure"`

	schema json.RawMessage // raw bytes of schema.json (served to the model as-is)
}

// Duration parses "8s"-style YAML values.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// Schema returns the tool's argument JSON Schema bytes.
func (m *Manifest) Schema() json.RawMessage { return m.schema }

var (
	nameRe    = regexp.MustCompile(`^[a-z][a-z0-9_]{1,47}$`)
	scopeRe   = regexp.MustCompile(`^[a-z][a-z0-9_]*\.(read|write|send|trade)$`)
	semverRe  = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	egressRe  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`) // symbolic egress classes only; no hosts or IP literals
	sideEff   = map[string]bool{"read": true, "write": true, "send": true}
	modeNames = map[string]bool{"auto": true, "force": true}
)

const maxTimeout = 30 * time.Second

// Validate enforces the manifest rules. It fails closed: any doubt is an error.
func (m *Manifest) Validate() error {
	switch {
	case !nameRe.MatchString(m.Name):
		return fmt.Errorf("invalid name %q", m.Name)
	case !semverRe.MatchString(m.Version):
		return fmt.Errorf("%s: version must be MAJOR.MINOR.PATCH", m.Name)
	case strings.TrimSpace(m.Description) == "":
		return fmt.Errorf("%s: description required", m.Name)
	case m.SideEffect == "trade":
		return fmt.Errorf("%s: side_effect trade is forbidden", m.Name)
	case !sideEff[m.SideEffect]:
		return fmt.Errorf("%s: invalid side_effect %q", m.Name, m.SideEffect)
	case ValidateVerbs(m.Verbs) != nil:
		return fmt.Errorf("%s: %w", m.Name, ValidateVerbs(m.Verbs))
	case len(m.Scopes) == 0:
		return fmt.Errorf("%s: at least one scope required", m.Name)
	case m.Timeout.Duration <= 0 || m.Timeout.Duration > maxTimeout:
		return fmt.Errorf("%s: timeout must be in (0, %s]", m.Name, maxTimeout)
	case m.MaxResultChars <= 0:
		return fmt.Errorf("%s: max_result_chars required", m.Name)
	case m.Exposure.MaxPerTurn <= 0:
		return fmt.Errorf("%s: exposure.max_per_turn required", m.Name)
	}
	for _, s := range m.Scopes {
		if !scopeRe.MatchString(s) {
			return fmt.Errorf("%s: invalid scope %q", m.Name, s)
		}
		if strings.HasSuffix(s, ".trade") {
			return fmt.Errorf("%s: trade scope is forbidden", m.Name)
		}
	}
	for _, e := range m.Egress {
		if !egressRe.MatchString(e) {
			return fmt.Errorf("%s: egress must be a symbolic class, got %q", m.Name, e)
		}
	}
	for _, md := range m.Exposure.Modes {
		if !modeNames[md] {
			return fmt.Errorf("%s: invalid exposure mode %q", m.Name, md)
		}
	}
	if len(m.Secrets) != 0 {
		return fmt.Errorf("%s: secrets are not supported before M2/M3", m.Name)
	}
	var sch map[string]any
	if err := json.Unmarshal(m.schema, &sch); err != nil {
		return fmt.Errorf("%s: schema.json: %w", m.Name, err)
	}
	if sch["type"] != "object" {
		return fmt.Errorf("%s: schema must be an object", m.Name)
	}
	return nil
}

// Registry is the immutable set of tools loaded at startup.
type Registry struct {
	byName map[string]*Manifest
	order  []string
}

//go:embed manifests
var builtinFS embed.FS

// LoadBuiltin loads the embedded manifests. Any invalid manifest fails the load.
func LoadBuiltin() (*Registry, error) {
	sub, _ := fs.Sub(builtinFS, "manifests")
	return LoadFS(sub)
}

// LoadFS loads <tool>/manifest.yaml + <tool>/schema.json from fsys.
func LoadFS(fsys fs.FS) (*Registry, error) {
	ents, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	r := &Registry{byName: map[string]*Manifest{}}
	for _, e := range ents {
		if !e.IsDir() {
			return nil, fmt.Errorf("unexpected file %q in manifests", e.Name())
		}
		m, err := loadOne(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		if _, dup := r.byName[m.Name]; dup {
			return nil, fmt.Errorf("duplicate tool %q", m.Name)
		}
		r.byName[m.Name] = m
		r.order = append(r.order, m.Name)
	}
	sort.Strings(r.order)
	return r, nil
}

func loadOne(fsys fs.FS, dir string) (*Manifest, error) {
	raw, err := fs.ReadFile(fsys, path.Join(dir, "manifest.yaml"))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // unknown fields are rejected
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s/manifest.yaml: %w", dir, err)
	}
	if m.Name != dir {
		return nil, fmt.Errorf("%s: manifest name %q must match directory", dir, m.Name)
	}
	sch, err := fs.ReadFile(fsys, path.Join(dir, "schema.json"))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	m.schema = json.RawMessage(bytes.TrimSpace(sch))
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Get returns the manifest for name.
func (r *Registry) Get(name string) (*Manifest, bool) {
	m, ok := r.byName[name]
	return m, ok
}

// Names returns all registered tool names, sorted.
func (r *Registry) Names() []string { return append([]string(nil), r.order...) }

// defsJSON renders the OpenAI tools array for the given names, in the given
// order, using the manifest description and the schema bytes verbatim.
func (r *Registry) defsJSON(names []string) string {
	var b strings.Builder
	b.WriteString("[\n")
	for i, n := range names {
		m := r.byName[n]
		desc, _ := json.Marshal(m.Description)
		fmt.Fprintf(&b, `{"type":"function","function":{"name":%q,"description":%s,"parameters":%s}}`, m.Name, desc, m.schema)
		if i < len(names)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("]")
	return b.String()
}
