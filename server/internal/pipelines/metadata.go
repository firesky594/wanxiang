package pipelines

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var safeID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var allowed = map[string]bool{"go": true, "npm": true, "pnpm": true, "node": true, "pm2": true}

func LoadDefinition(projectDir string) (Definition, error) {
	raw, err := os.ReadFile(filepath.Join(projectDir, ".wanxiang", "pipeline.json"))
	if err != nil {
		return Definition{}, err
	}
	var d Definition
	if json.Unmarshal(raw, &d) != nil || Validate(d) != nil {
		return Definition{}, ErrInvalidDefinition
	}
	return d, nil
}
func Validate(d Definition) error {
	seen := map[string]bool{}
	if len(d.Steps) == 0 || len(d.Steps) > 32 {
		return ErrInvalidDefinition
	}
	for _, s := range d.Steps {
		if !safeID.MatchString(s.ID) || seen[s.ID] || !allowed[s.Command] || s.TimeoutSeconds < 1 || s.TimeoutSeconds > 3600 || s.MaxAttempts < 1 || s.MaxAttempts > 5 {
			return ErrInvalidDefinition
		}
		seen[s.ID] = true
		if s.Kind != "test" && s.Kind != "build" && s.Kind != "release" && s.Kind != "migration" && s.Kind != "delete" {
			return ErrInvalidDefinition
		}
		for _, a := range s.Args {
			clean := filepath.Clean(a)
			if a == "" || strings.ContainsAny(a, "\n\r\x00|;&`$><") || filepath.IsAbs(a) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return ErrInvalidDefinition
			}
		}
		if (s.Kind == "release" || s.Kind == "migration" || s.Kind == "delete") && s.Reversible && s.Kind != "release" {
			return ErrInvalidDefinition
		}
	}
	return nil
}
func requiresConfirmation(kind string) bool {
	return kind == "release" || kind == "migration" || kind == "delete"
}
