package main

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
)

// seedLinkedDevice writes the whatsmeow device row a linked session would have.
func seedLinkedDevice(t *testing.T, storeDir, jid, lid string) {
	t.Helper()
	openSessionDB(t, storeDir, func(db *sql.DB) {
		if _, err := db.Exec(`CREATE TABLE whatsmeow_device (jid TEXT, lid TEXT)`); err != nil {
			t.Fatalf("Create table: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO whatsmeow_device (jid, lid) VALUES (?, ?)`, jid, lid); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	})
}

// seedLegacyLinkedDevice writes the same row as a whatsmeow old enough to have
// no `lid` column at all — a store shape this command has to survive.
func seedLegacyLinkedDevice(t *testing.T, storeDir, jid string) {
	t.Helper()
	openSessionDB(t, storeDir, func(db *sql.DB) {
		if _, err := db.Exec(`CREATE TABLE whatsmeow_device (jid TEXT)`); err != nil {
			t.Fatalf("Create table: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO whatsmeow_device (jid) VALUES (?)`, jid); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	})
}

func openSessionDB(t *testing.T, storeDir string, seed func(*sql.DB)) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(storeDir, "session.db"))
	if err != nil {
		t.Fatalf("Open session db: %v", err)
	}
	defer db.Close()
	seed(db)
}

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

// The identity a roster is actually written in. Without it the consumer cannot
// find the owner among participants that share no digits with the phone JID, so
// it keeps them in the census and the group never reaches "everyone has read".
func TestGroupsParticipantsListReportsBothSelfIdentities(t *testing.T) {
	storeDir := seedRosterStore(t)
	seedLinkedDevice(t, storeDir, "34600111222:23@s.whatsapp.net", "226822138650736:5@lid")

	payload := runRosterList(t, storeDir, "--jid", "120363000000000001@g.us")

	// Both arrive without the device suffix: it is not part of the identity, and
	// a consumer comparing whole strings would miss the owner because of it.
	if payload.SelfJID != "34600111222@s.whatsapp.net" {
		t.Fatalf("selfJid = %q", payload.SelfJID)
	}
	if payload.SelfLID != "226822138650736@lid" {
		t.Fatalf("selfLid = %q", payload.SelfLID)
	}
}

// A store an older whatsmeow wrote has no `lid` column. Reporting the phone JID
// and an empty LID is strictly better than failing the read: the roster is still
// the answer to the question that was asked.
func TestGroupsParticipantsListSurvivesAStoreWithoutTheLIDColumn(t *testing.T) {
	storeDir := seedRosterStore(t)
	seedLegacyLinkedDevice(t, storeDir, "34600111222@s.whatsapp.net")

	payload := runRosterList(t, storeDir, "--jid", "120363000000000001@g.us")

	if payload.SelfJID != "34600111222@s.whatsapp.net" || payload.SelfLID != "" {
		t.Fatalf("self identities = %q / %q", payload.SelfJID, payload.SelfLID)
	}
	if len(payload.Participants) != 2 {
		t.Fatalf("participants = %+v, want the roster regardless", payload.Participants)
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
