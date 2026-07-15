package leases

import "context"

func (s *Service) Authorize(ctx context.Context, ref LeaseRef, relativePath string) error {
	lease, err := loadLease(ctx, s.db, ref.LeaseID)
	if err != nil || !sameRef(lease.LeaseRef, ref) || lease.Status != LeaseActive || !s.clock.Now().UTC().Before(lease.ExpiresAt) {
		return ErrConflict
	}
	if s.workspaces == nil || s.workspaces.AuthorizeAgent(ctx, ref.AgentName, ref.TaskID, ref.StepID, relativePath) != nil {
		return ErrConflict
	}
	return nil
}
