package executor

import (
	"strings"
	"testing"
)

func TestRedactRemovesCommonSecretForms(t *testing.T) {
	input := "Authorization: Bearer abc.def.ghi\nAPI_KEY=sk-live-secret\npassword: hunter2\n{\"token\":\"json-secret\"}\nnormal validation error"
	got := Redact(input)
	for _, secret := range []string{"abc.def.ghi", "sk-live-secret", "hunter2", "json-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("secret leaked: %s in %q", secret, got)
		}
	}
	if !strings.Contains(got, "normal validation error") {
		t.Fatalf("normal text lost: %q", got)
	}
}

func TestRedactLimitsOutput(t *testing.T) {
	got := Redact(strings.Repeat("safe", maxRedactedBytes))
	if len(got) > maxRedactedBytes {
		t.Fatalf("length=%d", len(got))
	}
}
