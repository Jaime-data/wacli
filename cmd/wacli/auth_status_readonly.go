package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openclaw/wacli/internal/sqliteutil"
	"go.mau.fi/whatsmeow/types"
)

func readOnlyAuthStatus(storeDir string) (bool, string, error) {
	authed, jid, _, err := readOnlyAuthIdentity(storeDir)
	return authed, jid, err
}

// readOnlyAuthIdentity reports BOTH names WhatsApp gives the linked device.
//
// The phone JID and the LID share no digits, and which one a surface speaks is
// not ours to choose: group rosters are written in LIDs. A consumer handed only
// the phone JID cannot recognise the device in a roster at all, so it leaves the
// census long by one — and a census long by one never completes, silently.
//
// The LID is best-effort on purpose: `lid` is a newer column, and a store
// written by an older whatsmeow simply does not have it. An empty string is the
// honest report of "unknown", which degrades to the phone JID; failing the whole
// read would take the answer that IS known away with it.
func readOnlyAuthIdentity(storeDir string) (bool, string, string, error) {
	sessionPath := filepath.Join(storeDir, "session.db")
	if _, err := os.Stat(sessionPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, "", "", nil
		}
		return false, "", "", err
	}
	if strings.ContainsAny(sessionPath, "?#") {
		return false, "", "", fmt.Errorf("session path must not contain '?' or '#'")
	}
	db, err := sql.Open("sqlite3", readOnlySessionSQLiteURI(sessionPath))
	if err != nil {
		return false, "", "", fmt.Errorf("open session db: %w", err)
	}
	defer db.Close()

	var jid, lid sql.NullString
	err = db.QueryRow("SELECT jid, lid FROM whatsmeow_device LIMIT 1").Scan(&jid, &lid)
	if err != nil && isMissingColumnError(err) {
		// A store an older whatsmeow wrote. Ask again for what every version
		// has. Narrow on purpose: a locked or corrupt store must NOT land here
		// and quietly report "no LID", because the consumer downstream reads
		// that as "this device has no second name" and keeps itself in its own
		// census — a wrong answer wearing the clothes of an old schema.
		lid = sql.NullString{}
		err = db.QueryRow("SELECT jid FROM whatsmeow_device LIMIT 1").Scan(&jid)
	}
	switch {
	case err == nil:
		linked := strings.TrimSpace(jid.String)
		if linked == "" {
			return false, "", "", nil
		}
		parsed, err := types.ParseJID(linked)
		if err != nil {
			return false, "", "", fmt.Errorf("parse auth JID: %w", err)
		}
		return true, parsed.ToNonAD().String(), normalizeDeviceLID(lid.String), nil
	case errors.Is(err, sql.ErrNoRows):
		return false, "", "", nil
	default:
		return false, "", "", fmt.Errorf("read auth status: %w", err)
	}
}

// isMissingColumnError reports whether SQLite refused the query because the
// column does not exist, which is the one failure an older store is allowed to
// produce here. Everything else is a real problem and must surface.
func isMissingColumnError(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "no such column")
}

// normalizeDeviceLID never fails the read: an unparseable LID is reported as
// unknown, exactly like a store that has no column for it.
func normalizeDeviceLID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := types.ParseJID(trimmed)
	if err != nil {
		return ""
	}
	return parsed.ToNonAD().String()
}

func readOnlySessionSQLiteURI(path string) string {
	params := "mode=ro&_query_only=1&_busy_timeout=5000"
	if !sqliteSessionSidecarsExist(path) {
		params += "&immutable=1"
	}
	return sqliteutil.FileURI(path, params)
}

func sqliteSessionSidecarsExist(path string) bool {
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			return true
		}
	}
	return false
}
