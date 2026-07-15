package executor

import (
	"strings"
	"testing"
)

func TestProtocolParsesValidResponse(t *testing.T) {
	got, err := ParseProviderResponse(`{"version":1,"status":"continue","summary":"完成读取","actions":[{"type":"read_file","path":"src/main.go"}],"next_action":"继续实现"}`)
	if err != nil || got.Version != 1 || len(got.Actions) != 1 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestProtocolRejectsUnsafeResponses(t *testing.T) {
	cases := []string{
		`{"version":2,"status":"continue","summary":"x","actions":[],"next_action":"x"}`,
		`{"version":1,"status":"unknown","summary":"x","actions":[],"next_action":"x"}`,
		`{"version":1,"status":"continue","summary":"x","actions":[{"type":"shell","command":"rm"}],"next_action":"x"}`,
		`{"version":1,"status":"continue","summary":"x","actions":[{"type":"read_file","path":"../env"}],"next_action":"x"}`,
		`{"version":1,"status":"continue","summary":"API_KEY=secret","actions":[],"next_action":"x"}`,
		`{"version":1,"status":"continue","summary":"bad\u0000text","actions":[],"next_action":"x"}`,
		`{"version":1,"status":"continue","summary":"x","actions":[{"type":"write_file","path":"src/a.go","content":"` + strings.Repeat("x", maxProtocolContentBytes+1) + `"}],"next_action":"x"}`,
	}
	for i, raw := range cases {
		if _, err := ParseProviderResponse(raw); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestProtocolRejectsTrailingJSON(t *testing.T) {
	if _, err := ParseProviderResponse(`{"version":1,"status":"completed","summary":"完成","actions":[],"next_action":"等待"} {}`); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}
