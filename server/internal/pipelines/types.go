package pipelines

import "errors"

type StepDefinition struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Command        string   `json:"command"`
	Args           []string `json:"args"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	MaxAttempts    int      `json:"max_attempts"`
	Reversible     bool     `json:"reversible"`
}
type Definition struct {
	Steps []StepDefinition `json:"steps"`
}
type Run struct {
	ID, ProjectID                                                                       int64
	TaskID                                                                              *int64
	Status, SafeCommit, ArtifactHash, DefinitionHash, RequestedBy, CreatedAt, LastError string
	Steps                                                                               []Step
}
type Step struct {
	ID, RunID                                                            int64
	Key, Kind, Command, Status, FailureClass, OutputSummary, ConfirmedBy string
	Args                                                                 []string
	TimeoutSeconds, MaxAttempts, Attempt                                 int
	Reversible                                                           bool
}
type StartInput struct {
	ProjectID                               int64
	TaskID                                  *int64
	Definition                              Definition
	SafeCommit, IdempotencyKey, RequestedBy string
}

var ErrNotFound = errors.New("pipeline_not_found")
var ErrConfirmationRequired = errors.New("pipeline_confirmation_required")
var ErrInvalidDefinition = errors.New("invalid_pipeline_definition")
