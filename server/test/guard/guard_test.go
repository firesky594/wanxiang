package guard_test

import (
	"os"
	"testing"
)

// TestRequireArchiveRunner 防止裸命令跳过集中归档的后端测试。
func TestRequireArchiveRunner(t *testing.T) {
	if os.Getenv("WANXIANG_TEST_OVERLAY") != "1" {
		t.Fatal("后端测试已归档，请使用 ./test/run.sh ./...")
	}
}
