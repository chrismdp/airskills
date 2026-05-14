package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type movedSourceNotice struct {
	localName     string
	oldOwner      string
	oldSlug       string
	newOwner      string
	newSlug       string
	newSkillID    string
	inSyncSurface bool
	hidden        bool
}

func collectMovedSourceNotices(client *apiClient, state *SyncState, remoteSkills []apiSkill) []movedSourceNotice {
	remoteByID := map[string]apiSkill{}
	for _, skill := range remoteSkills {
		remoteByID[skill.Id.String()] = skill
	}

	var notices []movedSourceNotice
	for localName, entry := range state.Skills {
		if entry == nil || entry.Source == nil {
			continue
		}
		body, status, err := client.getWithStatus(fmt.Sprintf("/api/v1/resolve/%s/%s", entry.Source.Owner, entry.Source.Slug))
		if err != nil || status != http.StatusGone {
			continue
		}
		var resp struct {
			MovedTo *struct {
				Kind      string `json:"kind"`
				Slug      string `json:"slug"`
				SkillSlug string `json:"skill_slug"`
				SkillID   string `json:"skill_id"`
			} `json:"moved_to"`
		}
		_ = json.Unmarshal(body, &resp)
		notice := movedSourceNotice{
			localName: localName,
			oldOwner:  entry.Source.Owner,
			oldSlug:   entry.Source.Slug,
		}
		if resp.MovedTo == nil || resp.MovedTo.SkillID == "" || resp.MovedTo.Slug == "" {
			notice.hidden = true
			notices = append(notices, notice)
			continue
		}
		notice.newOwner = resp.MovedTo.Slug
		notice.newSkillID = resp.MovedTo.SkillID
		notice.newSlug = resp.MovedTo.SkillSlug
		if remote, ok := remoteByID[resp.MovedTo.SkillID]; ok {
			notice.inSyncSurface = true
			notice.newSlug = remote.Name
		}
		if notice.newSlug == "" {
			notice.newSlug = entry.Source.Slug
		}
		notices = append(notices, notice)
	}
	return notices
}

func printMovedSourceNotices(notices []movedSourceNotice) {
	for _, notice := range notices {
		fmt.Print(formatMovedSourceNotice(notice, !isTTY))
	}
}

func formatMovedSourceNotice(notice movedSourceNotice, agentMode bool) string {
	if notice.hidden {
		return fmt.Sprintf("  %s %s/%s: upstream archived. Local copy %q is kept; no new location is visible to this account.\n",
			yellow("!"), notice.oldOwner, notice.oldSlug, notice.localName)
	}
	oldPath := notice.oldOwner + "/" + notice.oldSlug
	newPath := notice.newOwner + "/" + notice.newSlug
	if notice.inSyncSurface {
		if agentMode {
			return fmt.Sprintf("  %s %s moved to %s, and the new skill is already in this sync surface. Ask the user before removing the stale local copy, then run: airskills rm %s\n",
				yellow("!"), oldPath, newPath, notice.localName)
		}
		return fmt.Sprintf("  %s %s moved to %s. The new skill is already syncing; run 'airskills rm %s' to remove the stale local copy.\n",
			yellow("!"), oldPath, newPath, notice.localName)
	}
	if agentMode {
		return fmt.Sprintf("  %s %s moved to %s. Ask the user whether to follow the move; if yes, run: airskills add %s\n",
			yellow("!"), oldPath, newPath, newPath)
	}
	return fmt.Sprintf("  %s %s moved to %s. Run 'airskills add %s' to follow it.\n",
		yellow("!"), oldPath, newPath, newPath)
}
