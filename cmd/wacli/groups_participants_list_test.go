package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
)

func seedRosterStore(t *testing.T) string {
	t.Helper()
	storeDir := t.TempDir()
	db, err := store.Open(filepath.Join(storeDir, "wacli.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	gid := "120363000000000001@g.us"
	if err := db.UpsertGroup(gid, "Roster Group", "34600111222@s.whatsapp.net", time.Now().UTC()); err != nil {
		t.Fatalf("UpsertGroup: %v", err)
	}
	if err := db.ReplaceGroupParticipants(gid, []store.GroupParticipant{
		{GroupJID: gid, UserJID: "34600111222@s.whatsapp.net", Role: "admin"},
		{GroupJID: gid, UserJID: "34600333444@s.whatsapp.net", Role: ""},
	}); err != nil {
		t.Fatalf("ReplaceGroupParticipants: %v", err)
	}
	return storeDir
}

// Unwraps the `{success, data, error}` envelope every --json command writes, so
// the assertions below read the payload the backend's parser actually receives.
func runRosterList(t *testing.T, storeDir string, args ...string) groupsParticipantsListPayload {
	t.Helper()
	cmd := newGroupsParticipantsListCmd(&rootFlags{
		storeDir: storeDir,
		timeout:  time.Minute,
		asJSON:   true,
	})
	stdout := captureRootStdout(t, func() {
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("groups participants list: %v", err)
		}
	})

	var envelope struct {
		Success bool                          `json:"success"`
		Data    groupsParticipantsListPayload `json:"data"`
		Error   *string                       `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("unmarshal %q: %v", stdout, err)
	}
	if !envelope.Success || envelope.Error != nil {
		t.Fatalf("envelope not successful: %s", stdout)
	}
	return envelope.Data
}

// The whole point of the command: a reader must not need the write lock the
// `sync --follow` daemon holds, so read-only mode has to be a supported mode
// rather than a rejected one.
func TestGroupsParticipantsListWorksInReadOnlyMode(t *testing.T) {
	storeDir := seedRosterStore(t)

	cmd := newGroupsParticipantsListCmd(&rootFlags{
		storeDir: storeDir,
		timeout:  time.Minute,
		readOnly: true,
		asJSON:   true,
	})
	stdout := captureRootStdout(t, func() {
		cmd.SetArgs([]string{"--jid", "120363000000000001@g.us"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("read-only groups participants list: %v", err)
		}
	})
	if !strings.Contains(stdout, "34600333444@s.whatsapp.net") {
		t.Fatalf("read-only output missing participant: %s", stdout)
	}
}

func TestGroupsParticipantsListReportsRosterAndRoles(t *testing.T) {
	payload := runRosterList(t, seedRosterStore(t), "--jid", "120363000000000001@g.us")

	if payload.GroupJID != "120363000000000001@g.us" {
		t.Fatalf("groupJid = %q", payload.GroupJID)
	}
	if len(payload.Participants) != 2 {
		t.Fatalf("participants = %+v, want 2", payload.Participants)
	}
	// Ordered by user_jid, so the assertions can be exact.
	if payload.Participants[0].UserJID != "34600111222@s.whatsapp.net" || payload.Participants[0].Role != "admin" {
		t.Fatalf("first participant = %+v", payload.Participants[0])
	}
	// A stored NULL/empty role reads back as `member`, never as an empty string:
	// a consumer must not have to guess what "" means.
	if payload.Participants[1].Role != "member" {
		t.Fatalf("second participant role = %q, want member", payload.Participants[1].Role)
	}
	if payload.Participants[1].UpdatedAt == "" {
		t.Fatalf("expected updatedAt on %+v", payload.Participants[1])
	}
}

// An unknown group must answer "empty", not error: the caller cannot tell an
// unknown group from an empty one any other way, and an error would look like a
// broken store.
func TestGroupsParticipantsListReturnsEmptyForUnknownGroup(t *testing.T) {
	payload := runRosterList(t, seedRosterStore(t), "--jid", "120363000000000999@g.us")

	if payload.GroupJID != "120363000000000999@g.us" {
		t.Fatalf("groupJid = %q, want the JID that was asked for", payload.GroupJID)
	}
	if len(payload.Participants) != 0 {
		t.Fatalf("participants = %+v, want none", payload.Participants)
	}
}

func TestGroupsParticipantsListRejectsMissingAndNonGroupJID(t *testing.T) {
	storeDir := seedRosterStore(t)
	for name, args := range map[string][]string{
		"missing":   {},
		"non group": {"--jid", "34600111222@s.whatsapp.net"},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := newGroupsParticipantsListCmd(&rootFlags{storeDir: storeDir, timeout: time.Minute})
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil {
				t.Fatalf("expected an error for %v", args)
			}
		})
	}
}
