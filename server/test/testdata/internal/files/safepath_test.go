package files

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestUnderRootAllowsBasenameStartingWithTwoDots(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "..notes")

	got, err := UnderRoot(root, target)
	if err != nil {
		t.Fatalf("UnderRoot: %v", err)
	}
	if got != target {
		t.Fatalf("got=%q want=%q", got, target)
	}
}

func TestUnderRootRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := UnderRoot(root, filepath.Join(root, "..", "escape")); err == nil {
		t.Fatalf("parent traversal should fail")
	}
}

func TestUnderRootRejectsSymlinkComponentPointingOutside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatalf("Symlink: %v", err)
	}

	if _, err := UnderRoot(root, filepath.Join(link, "escaped.txt")); err == nil {
		t.Fatalf("symlink component should fail")
	}
}
