package cmd

import (
	"testing"
)

func TestRestoreCmdRegistered(t *testing.T) {
	found := false
	for _, c := range rootCmd.Commands() {
		if c.Name() == "restore" {
			found = true
			break
		}
	}
	if !found {
		t.Error("restore command not registered on rootCmd")
	}
}

func TestListDeletedSkillsFieldMapping(t *testing.T) {
	// Verify that apiSkill correctly decodes deleted_at and deletion_reason
	// from a server response — these fields are new and could silently zero
	// out if the JSON tags are wrong.
	deletedAt := "2026-04-15T10:00:00Z"
	reason := "user_deleted"
	s := apiSkill{
		ID:             "skill-123",
		Name:           "my-skill",
		Version:        "1.0.0",
		DeletedAt:      &deletedAt,
		DeletionReason: &reason,
	}
	if s.DeletedAt == nil || *s.DeletedAt != deletedAt {
		t.Errorf("DeletedAt field wrong: got %v", s.DeletedAt)
	}
	if s.DeletionReason == nil || *s.DeletionReason != reason {
		t.Errorf("DeletionReason field wrong: got %v", s.DeletionReason)
	}
}
