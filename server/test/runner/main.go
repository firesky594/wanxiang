package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type overlayConfig struct {
	Replace map[string]string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	serverRoot, err := filepath.Abs(".")
	if err != nil {
		return err
	}
	caseRoot := filepath.Join(serverRoot, "test", "testdata")
	guardRoot := filepath.Join(serverRoot, "test", "guard")
	if info, statErr := os.Stat(caseRoot); statErr != nil || !info.IsDir() {
		return fmt.Errorf("测试归档目录不可用: %s", caseRoot)
	}
	if err := rejectMisplacedTests(serverRoot, caseRoot, guardRoot); err != nil {
		return err
	}

	replacements := make(map[string]string)
	err = filepath.WalkDir(caseRoot, func(source string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		relativePath, relErr := filepath.Rel(caseRoot, source)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(serverRoot, relativePath)
		if _, statErr := os.Stat(filepath.Dir(target)); statErr != nil {
			return fmt.Errorf("测试目标包不存在: %s", filepath.Dir(target))
		}
		if _, statErr := os.Stat(target); statErr == nil {
			return fmt.Errorf("源码目录已存在测试文件，拒绝覆盖: %s", target)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		replacements[target] = source
		return nil
	})
	if err != nil {
		return err
	}
	if len(replacements) == 0 {
		return errors.New("没有找到可执行的 Go 测试文件")
	}

	overlayFile, err := os.CreateTemp("", "wanxiang-go-test-overlay-*.json")
	if err != nil {
		return err
	}
	overlayPath := overlayFile.Name()
	defer os.Remove(overlayPath)

	encoder := json.NewEncoder(overlayFile)
	if err := encoder.Encode(overlayConfig{Replace: replacements}); err != nil {
		overlayFile.Close()
		return err
	}
	if err := overlayFile.Close(); err != nil {
		return err
	}

	testArgs := os.Args[1:]
	if len(testArgs) == 0 {
		testArgs = []string{"./..."}
	}
	for _, arg := range testArgs {
		if arg == "-overlay" || strings.HasPrefix(arg, "-overlay=") {
			return errors.New("测试 overlay 由归档运行器管理，禁止覆盖")
		}
	}
	args := append([]string{"test", "-overlay=" + overlayPath}, testArgs...)
	fmt.Printf("加载归档测试: %d 个文件\n", len(replacements))
	command := exec.Command("go", args...)
	command.Dir = serverRoot
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Env = append(os.Environ(), "WANXIANG_TEST_OVERLAY=1")
	return command.Run()
}

func rejectMisplacedTests(serverRoot, caseRoot, guardRoot string) error {
	return filepath.WalkDir(serverRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && (path == caseRoot || path == guardRoot) {
			return filepath.SkipDir
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), "_test.go") {
			return fmt.Errorf("测试文件必须归档到 test/testdata: %s", path)
		}
		return nil
	})
}
