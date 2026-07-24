package pipelines

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var safeID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// LoadDefinition 读取并校验项目流水线定义。
func LoadDefinition(projectDir string) (Definition, error) {
	meta := filepath.Join(projectDir, ".wanxiang")
	if info, err := os.Lstat(meta); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return Definition{}, ErrInvalidDefinition
	}
	path := filepath.Join(meta, "pipeline.json")
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return Definition{}, ErrInvalidDefinition
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, err
	}
	var d Definition
	if json.Unmarshal(raw, &d) != nil || Validate(d) != nil {
		return Definition{}, ErrInvalidDefinition
	}
	return d, nil
}

// Validate 校验流水线步骤、参数与发布约束。
func Validate(d Definition) error {
	seen := map[string]bool{}
	buildArtifact := ""
	buildCount, releaseCount := 0, 0
	if len(d.Steps) == 0 || len(d.Steps) > 32 {
		return ErrInvalidDefinition
	}
	for _, s := range d.Steps {
		if !safeID.MatchString(s.ID) || seen[s.ID] || !allowedStep(s) || s.TimeoutSeconds < 1 || s.TimeoutSeconds > 3600 || s.MaxAttempts < 1 || s.MaxAttempts > 5 {
			return ErrInvalidDefinition
		}
		seen[s.ID] = true
		for _, a := range s.Args {
			clean := filepath.Clean(a)
			if a == "" || strings.ContainsAny(a, "\n\r\x00|;&`$><") || filepath.IsAbs(a) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return ErrInvalidDefinition
			}
		}
		if s.Artifact != "" {
			clean := filepath.Clean(s.Artifact)
			if filepath.IsAbs(s.Artifact) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return ErrInvalidDefinition
			}
		}
		if s.Kind == "build" {
			buildCount++
			if s.Artifact != "" {
				buildArtifact = s.Artifact
			}
		}
		if s.Kind == "release" && (buildArtifact == "" || !validHealthURL(s.HealthURL)) {
			return ErrInvalidDefinition
		}
		if s.Kind == "release" {
			releaseCount++
		}
		if s.Kind == "migration" || s.Kind == "delete" {
			return ErrInvalidDefinition
		}
	}
	if buildCount > 1 || releaseCount > 1 {
		return ErrInvalidDefinition
	}
	return nil
}

func validHealthURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost") && u.Port() != "" && u.Path != ""
}
func allowedStep(s StepDefinition) bool {
	switch s.Kind {
	case "test":
		return (s.Command == "go" && len(s.Args) >= 2 && s.Args[0] == "test" && safeGoArgs(s.Args[1:])) || ((s.Command == "npm" || s.Command == "pnpm") && safeNPMTestArgs(s.Args))
	case "build":
		return (s.Command == "go" && len(s.Args) >= 2 && s.Args[0] == "build" && safeGoArgs(s.Args[1:])) || ((s.Command == "npm" || s.Command == "pnpm") && len(s.Args) == 2 && s.Args[0] == "run" && s.Args[1] == "build")
	case "release":
		return s.Command == "pm2" && len(s.Args) == 2 && s.Args[0] == "restart" && safeID.MatchString(s.Args[1]) && s.Reversible
	}
	return false
}

var safePackage = regexp.MustCompile(`^(\./\.\.\.|\./[A-Za-z0-9_./-]*|[A-Za-z0-9_./-]+|-count=[1-9][0-9]*|-timeout=[1-9][0-9]*s|-buildvcs=false)$`)

func safeGoArgs(args []string) bool {
	for _, a := range args {
		if !safePackage.MatchString(a) {
			return false
		}
	}
	return true
}
func safeNPMTestArgs(args []string) bool {
	if len(args) == 1 {
		return args[0] == "test"
	}
	return len(args) == 3 && args[0] == "test" && args[1] == "--" && args[2] == "--run"
}
func requiresConfirmation(kind string) bool {
	return kind == "release" || kind == "migration" || kind == "delete"
}
