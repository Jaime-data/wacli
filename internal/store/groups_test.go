package store

import (
	"testing"
	"time"
)

func TestGroupsUpsertListAndParticipantsReplace(t *testing.T) {
	db := openTestDB(t)

	gid := "123@g.us"
	created := time.Date(2023, 12, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertGroup(gid, "Group", "owner@s.whatsapp.net", created); err != nil {
		t.Fatalf("UpsertGroup: %v", err)
	}
	if err := db.ReplaceGroupParticipants(gid, []GroupParticipant{
		{GroupJID: gid, UserJID: "a@s.whatsapp.net", Role: "admin"},
		{GroupJID: gid, UserJID: "b@s.whatsapp.net", Role: ""},
	}); err != nil {
		t.Fatalf("ReplaceGroupParticipants: %v", err)
	}

	gs, err := db.ListGroups("Gro", 10)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(gs) != 1 || gs[0].JID != gid {
		t.Fatalf("expected group in list, got %+v", gs)
	}

	admins := countRows(t, db.sql, "SELECT COUNT(*) FROM group_participants WHERE group_jid=? AND role='admin'", gid)
	members := countRows(t, db.sql, "SELECT COUNT(*) FROM group_participants WHERE group_jid=? AND role='member'", gid)
	if admins != 1 || members != 1 {
		t.Fatalf("expected roles admin=1 member=1, got admin=%d member=%d", admins, members)
	}
}

func TestListGroupParticipantsReadsTheStoredRoster(t *testing.T) {
	db := openTestDB(t)

	gid := "123@g.us"
	if err := db.UpsertGroup(gid, "Group", "owner@s.whatsapp.net", time.Now().UTC()); err != nil {
		t.Fatalf("UpsertGroup: %v", err)
	}
	if err := db.ReplaceGroupParticipants(gid, []GroupParticipant{
		{GroupJID: gid, UserJID: "b@s.whatsapp.net", Role: "admin"},
		{GroupJID: gid, UserJID: "a@s.whatsapp.net", Role: ""},
	}); err != nil {
		t.Fatalf("ReplaceGroupParticipants: %v", err)
	}

	ps, err := db.ListGroupParticipants(gid)
	if err != nil {
		t.Fatalf("ListGroupParticipants: %v", err)
	}
	if len(ps) != 2 {
		t.Fatalf("participants = %+v, want 2", ps)
	}
	// Sorted by JID so a consumer comparing two reads sees a stable census.
	if ps[0].UserJID != "a@s.whatsapp.net" || ps[1].UserJID != "b@s.whatsapp.net" {
		t.Fatalf("unsorted participants: %+v", ps)
	}
	if ps[0].Role != "member" || ps[1].Role != "admin" {
		t.Fatalf("roles = %q/%q, want member/admin", ps[0].Role, ps[1].Role)
	}
	if ps[0].UpdatedAt.IsZero() {
		t.Fatalf("expected UpdatedAt on %+v", ps[0])
	}

	// A replace is authoritative: a departed member must not linger in a census
	// that decides whether "everybody" has read a message.
	if err := db.ReplaceGroupParticipants(gid, []GroupParticipant{
		{GroupJID: gid, UserJID: "a@s.whatsapp.net", Role: "member"},
	}); err != nil {
		t.Fatalf("ReplaceGroupParticipants (shrink): %v", err)
	}
	ps, err = db.ListGroupParticipants(gid)
	if err != nil {
		t.Fatalf("ListGroupParticipants after shrink: %v", err)
	}
	if len(ps) != 1 || ps[0].UserJID != "a@s.whatsapp.net" {
		t.Fatalf("participants after shrink = %+v, want only a", ps)
	}
}

func TestListGroupParticipantsUnknownGroupIsEmptyNotAnError(t *testing.T) {
	db := openTestDB(t)

	ps, err := db.ListGroupParticipants("nope@g.us")
	if err != nil {
		t.Fatalf("ListGroupParticipants: %v", err)
	}
	if len(ps) != 0 {
		t.Fatalf("participants = %+v, want none", ps)
	}
	if _, err := db.ListGroupParticipants("  "); err == nil {
		t.Fatal("expected an error for a blank JID")
	}
}

func TestGroupsUpsertHierarchy(t *testing.T) {
	db := openTestDB(t)

	parent := "parent@g.us"
	child := "child@g.us"
	created := time.Date(2024, 2, 3, 4, 5, 6, 0, time.UTC)

	if err := db.UpsertGroupWithHierarchy(parent, "Community", "", created, true, "ignored@g.us"); err != nil {
		t.Fatalf("UpsertGroupWithHierarchy parent: %v", err)
	}
	if err := db.UpsertGroupWithHierarchy(child, "Subgroup", "", created, false, parent); err != nil {
		t.Fatalf("UpsertGroupWithHierarchy child: %v", err)
	}

	gs, err := db.ListGroups("", 10)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	byJID := map[string]Group{}
	for _, g := range gs {
		byJID[g.JID] = g
	}
	if got := byJID[parent]; !got.IsParent || got.LinkedParentJID != "" {
		t.Fatalf("parent hierarchy = %+v, want parent without linked parent", got)
	}
	if got := byJID[child]; got.IsParent || got.LinkedParentJID != parent {
		t.Fatalf("child hierarchy = %+v, want linked parent %q", got, parent)
	}

	if err := db.UpsertGroup(child, "Renamed", "", time.Time{}); err != nil {
		t.Fatalf("UpsertGroup preserve hierarchy: %v", err)
	}
	gs, err = db.ListGroups("", 10)
	if err != nil {
		t.Fatalf("ListGroups after preserve: %v", err)
	}
	for _, g := range gs {
		if g.JID == child && g.LinkedParentJID != parent {
			t.Fatalf("plain upsert should preserve hierarchy, got %+v", g)
		}
	}

	if err := db.UpsertGroupWithHierarchy(child, "Subgroup", "", created, false, ""); err != nil {
		t.Fatalf("UpsertGroupWithHierarchy clear: %v", err)
	}
	gs, err = db.ListGroups("", 10)
	if err != nil {
		t.Fatalf("ListGroups after clear: %v", err)
	}
	for _, g := range gs {
		if g.JID == child && g.LinkedParentJID != "" {
			t.Fatalf("hierarchy clear failed, got %+v", g)
		}
	}
}

func TestGroupsLeftStateHiddenUntilRefreshed(t *testing.T) {
	db := openTestDB(t)

	gid := "123@g.us"
	created := time.Date(2023, 12, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertGroup(gid, "Group", "owner@s.whatsapp.net", created); err != nil {
		t.Fatalf("UpsertGroup: %v", err)
	}
	if err := db.MarkGroupLeft(gid, time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)); err != nil {
		t.Fatalf("MarkGroupLeft: %v", err)
	}

	gs, err := db.ListGroups("", 10)
	if err != nil {
		t.Fatalf("ListGroups after left: %v", err)
	}
	if len(gs) != 0 {
		t.Fatalf("left group should be hidden, got %+v", gs)
	}

	if err := db.UpsertGroup(gid, "Group", "owner@s.whatsapp.net", created); err != nil {
		t.Fatalf("UpsertGroup refresh: %v", err)
	}
	gs, err = db.ListGroups("", 10)
	if err != nil {
		t.Fatalf("ListGroups after refresh: %v", err)
	}
	if len(gs) != 1 || gs[0].JID != gid || !gs[0].LeftAt.IsZero() {
		t.Fatalf("refresh should restore group, got %+v", gs)
	}
}

func TestGroupsUsesInjectedClockForUpdates(t *testing.T) {
	db := openTestDB(t)
	fixed := time.Date(2025, 6, 7, 8, 9, 10, 0, time.UTC)
	oldNow := nowUTC
	nowUTC = func() time.Time { return fixed }
	t.Cleanup(func() { nowUTC = oldNow })

	gid := "123@g.us"
	if err := db.UpsertGroup(gid, "Group", "", time.Time{}); err != nil {
		t.Fatalf("UpsertGroup: %v", err)
	}
	gs, err := db.ListGroups("", 10)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(gs) != 1 || !gs[0].UpdatedAt.Equal(fixed) {
		t.Fatalf("UpdatedAt = %+v, want %s", gs, fixed)
	}
}

func TestMarkGroupsMissingFrom(t *testing.T) {
	db := openTestDB(t)

	if err := db.UpsertGroup("active@g.us", "Active", "", time.Time{}); err != nil {
		t.Fatalf("UpsertGroup active: %v", err)
	}
	if err := db.UpsertGroup("left@g.us", "Left", "", time.Time{}); err != nil {
		t.Fatalf("UpsertGroup left: %v", err)
	}

	if err := db.MarkGroupsMissingFrom(map[string]bool{"active@g.us": true}, time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)); err != nil {
		t.Fatalf("MarkGroupsMissingFrom: %v", err)
	}
	gs, err := db.ListGroups("", 10)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(gs) != 1 || gs[0].JID != "active@g.us" {
		t.Fatalf("expected only active group, got %+v", gs)
	}
}
