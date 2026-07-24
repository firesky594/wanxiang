package files

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// UnderRoot 解析根目录内安全路径并拒绝链接逃逸。
func UnderRoot(root, target string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("path escapes root")
	}
	if err := rejectLinkComponents(absRoot, rel); err != nil {
		return "", err
	}
	return absTarget, nil
}

func rejectLinkComponents(root, rel string) error {
	current := root
	parts := append([]string{"."}, strings.Split(rel, string(filepath.Separator))...)
	for _, part := range parts {
		if part != "." {
			current = filepath.Join(current, part)
		}
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if isReparsePoint(info) {
			return errors.New("path contains symlink or reparse point")
		}
	}
	return nil
}
