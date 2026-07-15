package executor

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	maxProtocolBytes        = 256 * 1024
	maxProtocolTextBytes    = 2000
	maxProtocolContentBytes = 32 * 1024
)

type ProviderStatus string

const (
	ProviderContinue   ProviderStatus = "continue"
	ProviderCheckpoint ProviderStatus = "checkpoint"
	ProviderCompleted  ProviderStatus = "completed"
	ProviderBlocked    ProviderStatus = "blocked"
)

type ProviderResponse struct {
	Version    int             `json:"version"`
	Status     ProviderStatus  `json:"status"`
	Summary    string          `json:"summary"`
	Actions    []ActionRequest `json:"actions"`
	NextAction string          `json:"next_action"`
}

func ParseProviderResponse(raw string) (ProviderResponse, error) {
	if len(raw) == 0 || len(raw) > maxProtocolBytes || !utf8.ValidString(raw) {
		return ProviderResponse{}, errors.New("invalid provider response size")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result ProviderResponse
	if err := decoder.Decode(&result); err != nil {
		return ProviderResponse{}, errors.New("invalid provider JSON")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return ProviderResponse{}, err
	}
	if result.Version != 1 {
		return ProviderResponse{}, errors.New("unsupported protocol version")
	}
	switch result.Status {
	case ProviderContinue, ProviderCheckpoint, ProviderCompleted, ProviderBlocked:
	default:
		return ProviderResponse{}, errors.New("invalid provider status")
	}
	if unsafeProtocolText(result.Summary) || unsafeProtocolText(result.NextAction) {
		return ProviderResponse{}, errors.New("unsafe provider summary")
	}
	if len(result.Actions) > 16 {
		return ProviderResponse{}, errors.New("too many provider actions")
	}
	for _, action := range result.Actions {
		if !action.Type.Valid() {
			return ProviderResponse{}, errors.New("unknown provider action")
		}
		if len(action.Content) > maxProtocolContentBytes || hasProtocolControl(action.Content) || Redact(action.Content) != action.Content {
			return ProviderResponse{}, errors.New("unsafe action content")
		}
		switch action.Type {
		case ActionReadFile, ActionWriteFile:
			if _, err := validateWorkerPath(action.Path); err != nil {
				return ProviderResponse{}, err
			}
		case ActionRunCheck:
			if err := validateCheck(CheckRequest{Command: action.Command, Args: action.Args}); err != nil {
				return ProviderResponse{}, err
			}
		}
	}
	return result, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("provider response has trailing JSON")
	}
	return nil
}
func unsafeProtocolText(value string) bool {
	return strings.TrimSpace(value) == "" || len(value) > maxProtocolTextBytes || hasProtocolControl(value) || Redact(value) != value
}
func hasProtocolControl(value string) bool {
	for _, char := range value {
		if char < 32 && char != '\n' && char != '\t' || char == 127 {
			return true
		}
	}
	return false
}
