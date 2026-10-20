package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// All contacts, identifiers, and messages here are synthetic. The full phone
// number uses the reserved fictional 202-555-0123 number.
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(dir, "ChatStorage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`PRAGMA journal_mode=WAL;
CREATE TABLE ZWACHATSESSION (Z_PK INTEGER PRIMARY KEY,ZCONTACTJID TEXT,ZPARTNERNAME TEXT,ZLASTMESSAGEDATE REAL,ZLASTMESSAGETEXT TEXT,ZARCHIVED INTEGER,ZHIDDEN INTEGER,ZUNREADCOUNT INTEGER);
CREATE TABLE ZWAMESSAGE (Z_PK INTEGER PRIMARY KEY,ZSTANZAID TEXT,ZCHATSESSION INTEGER,ZMESSAGEDATE REAL,ZISFROMME INTEGER,ZTEXT TEXT,ZMESSAGETYPE INTEGER,ZPUSHNAME TEXT,ZFROMJID TEXT,ZGROUPMEMBER INTEGER,ZMEDIAITEM INTEGER);
CREATE TABLE ZWAGROUPMEMBER (Z_PK INTEGER PRIMARY KEY,ZMEMBERJID TEXT,ZCONTACTNAME TEXT);
CREATE TABLE ZWAMEDIAITEM (Z_PK INTEGER PRIMARY KEY,ZMEDIALOCALPATH TEXT,ZTITLE TEXT);
INSERT INTO ZWACHATSESSION VALUES (1,'123@lid','Alice',800000000,'latest',0,0,4),(2,'group@g.us','Project team',800000000,'latest',1,0,0),(3,'222@s.whatsapp.net','Alice',800000000,'hidden',0,1,0);
INSERT INTO ZWAGROUPMEMBER VALUES (1,'123@lid','cached group name');
INSERT INTO ZWAMEDIAITEM VALUES (1,'Media/example.txt','report');`)
	if err != nil {
		t.Fatal(err)
	}
	base := float64(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix() - appleEpoch)
	messages := []struct {
		id, chat             int
		text                 string
		date                 float64
		fromMe, group, media int
		sender               string
	}{
		{10, 1, "First message", base, 0, 0, 0, "123@lid"},
		{11, 1, "O'Brien 100%_ test", base + 60, 0, 0, 0, "123@lid"},
		{12, 1, "Über FLIGHT", base + 60, 1, 0, 0, ""},
		{13, 1, "Latest\nline two", base + 120, 0, 0, 0, "123@lid"},
		{14, 2, "team flight", base + 180, 0, 1, 0, ""},
		{15, 2, "", base + 240, 0, 1, 1, ""},
		{16, 3, "hidden", base + 300, 0, 0, 0, "222@s.whatsapp.net"},
	}
	for _, m := range messages {
		_, err = db.Exec(`INSERT INTO ZWAMESSAGE VALUES (?,?,?,?,?,?,0,'fallback',?,NULLIF(?,0),NULLIF(?,0))`, m.id, "stanza-"+string(rune(m.id+'a')), m.chat, m.date, m.fromMe, m.text, m.sender, m.group, m.media)
		if err != nil {
			t.Fatal(err)
		}
	}
	contacts, err := sql.Open("sqlite3", filepath.Join(dir, "ContactsV2.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer contacts.Close()
	_, err = contacts.Exec(`CREATE TABLE ZWAADDRESSBOOKCONTACT (Z_PK INTEGER PRIMARY KEY,ZFULLNAME TEXT,ZGIVENNAME TEXT,ZBUSINESSNAME TEXT,ZPHONENUMBER TEXT,ZLOCALIZEDPHONENUMBER TEXT,ZWHATSAPPID TEXT,ZLID TEXT);
INSERT INTO ZWAADDRESSBOOKCONTACT VALUES (1,'Alice Contact',NULL,NULL,'12025550123',NULL,'12025550123@s.whatsapp.net','123@lid');
INSERT INTO ZWAADDRESSBOOKCONTACT VALUES (2,'No chat contact',NULL,NULL,'444',NULL,'444@s.whatsapp.net',NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "Media"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Media", "example.txt"), []byte("fixture attachment"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func commandJSON(t *testing.T, dir string, dest any, args ...string) {
	t.Helper()
	var out, errOut bytes.Buffer
	args = append(args, "--store", dir, "--json")
	if err := run(args, &out, &errOut); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, errOut.String())
	}
	if err := json.Unmarshal(out.Bytes(), dest); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
}

func TestReadSearchAndContacts(t *testing.T) {
	dir := fixture(t)
	var messages []Message
	commandJSON(t, dir, &messages, "read", "+1 (202) 555-0123", "--limit", "2")
	if len(messages) != 2 || messages[0].ID != 12 || messages[1].ID != 13 || messages[0].Sender != "Me" || messages[1].Sender != "Alice Contact" {
		t.Fatalf("unexpected read results: %+v", messages)
	}
	commandJSON(t, dir, &messages, "read", "Alice Contact", "--limit", "1")
	if len(messages) != 1 || messages[0].ChatID != 1 {
		t.Fatal("saved contact name didn't resolve to its conversation")
	}
	commandJSON(t, dir, &messages, "read", "1", "--limit", "2", "--offset", "2")
	if len(messages) != 2 || messages[0].ID != 10 || messages[1].ID != 11 {
		t.Fatalf("wrong page: %+v", messages)
	}
	commandJSON(t, dir, &messages, "search", "FLIGHT")
	if len(messages) != 2 || messages[0].ID != 14 || messages[1].ID != 12 {
		t.Fatalf("search: %+v", messages)
	}
	commandJSON(t, dir, &messages, "search", "über", "--chat", "1")
	if len(messages) != 1 || messages[0].ID != 12 {
		t.Fatalf("Unicode search: %+v", messages)
	}
	commandJSON(t, dir, &messages, "search", "O'Brien 100%_")
	if len(messages) != 1 || messages[0].ID != 11 {
		t.Fatalf("literal search: %+v", messages)
	}
	commandJSON(t, dir, &messages, "search", "'; DROP TABLE ZWAMESSAGE; --")
	if len(messages) != 0 {
		t.Fatal("SQL injection text matched")
	}
	commandJSON(t, dir, &messages, "read", "1", "--after", "2026-01-01T00:01:00Z", "--before", "2026-01-01T00:02:00Z")
	if len(messages) != 2 || messages[0].ID != 11 || messages[0].Timestamp != "2026-01-01T00:01:00Z" {
		t.Fatalf("date boundaries: %+v", messages)
	}
	var contacts []Contact
	commandJSON(t, dir, &contacts, "contacts", "No chat")
	if len(contacts) != 1 || contacts[0].Phone != "444" {
		t.Fatalf("contacts without chats: %+v", contacts)
	}
}

func TestContextMediaAndAmbiguity(t *testing.T) {
	dir := fixture(t)
	var context struct {
		Message       Message
		Before, After []Message
	}
	commandJSON(t, dir, &context, "context", "11", "--around", "1")
	if context.Message.ID != 11 || len(context.Before) != 1 || context.Before[0].ID != 10 || len(context.After) != 1 || context.After[0].ID != 12 {
		t.Fatalf("same-time context ordering: %+v", context)
	}
	var media struct {
		MessageID int64 `json:"message_id"`
		Path      string
		Exists    bool
	}
	commandJSON(t, dir, &media, "media", "15")
	if !media.Exists || media.Path != filepath.Join(dir, "Media", "example.txt") {
		t.Fatalf("media path: %+v", media)
	}
	var out bytes.Buffer
	err := run([]string{"read", "Alice", "--store", dir}, &out, &out)
	if err == nil || !strings.Contains(err.Error(), "matches 2 chats") {
		t.Fatalf("ambiguous name wasn't rejected: %v", err)
	}
	var chats []Chat
	commandJSON(t, dir, &chats, "chats")
	if len(chats) != 2 {
		t.Fatalf("hidden chats were listed: %+v", chats)
	}
	commandJSON(t, dir, &chats, "chats", "--all")
	if len(chats) != 3 {
		t.Fatal("--all didn't include hidden chats")
	}
}

func TestReadOnlyAndLiveWAL(t *testing.T) {
	dir := fixture(t)
	path := filepath.Join(dir, "ChatStorage.sqlite")
	hash := func(path string) [32]byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return sha256.Sum256(data)
	}
	before, beforeWAL := hash(path), hash(path+"-wal")
	a, err := openArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.db.Close()
	var count int
	if err := a.db.QueryRow("SELECT COUNT(*) FROM ZWAMESSAGE").Scan(&count); err != nil || count != 7 {
		t.Fatalf("live WAL not read: %d %v", count, err)
	}
	for _, query := range []string{"INSERT INTO ZWAMESSAGE (Z_PK) VALUES (99)", "DELETE FROM ZWAMESSAGE", "PRAGMA user_version=99"} {
		if _, err := a.db.Exec(query); err == nil {
			t.Fatalf("write succeeded: %s", query)
		}
	}
	if hash(path) != before || hash(path+"-wal") != beforeWAL {
		t.Fatal("source database or WAL modified")
	}
	missing := filepath.Join(dir, "missing.sqlite")
	if db, err := openReadOnly(missing); err == nil {
		db.Close()
		t.Fatal("missing database opened")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("created missing source file")
	}
}

func TestErrorsAndOptionParsing(t *testing.T) {
	for _, args := range [][]string{{"read"}, {"search"}, {"context"}, {"sync"}, {"chats", "--limit", "0"}, {"chats", "--offset", "-1"}, {"chats", "--wat"}, {"chats", "--limit"}} {
		var b bytes.Buffer
		if err := run(args, &b, &b); err == nil {
			t.Fatalf("accepted invalid arguments %v", args)
		}
	}
	var b bytes.Buffer
	if err := run([]string{"--help", "--db", "/missing"}, &b, &b); err != nil || !strings.Contains(b.String(), "wa read") {
		t.Fatal("help needs database")
	}
	dir := fixture(t)
	for _, args := range [][]string{{"read", "1", "--after", "bad-date"}, {"read", "1", "--after", "2027-01-01", "--before", "2026-01-01"}, {"read", "missing"}, {"media", "10"}} {
		args = append(args, "--store", dir)
		var b bytes.Buffer
		if err := run(args, &b, &b); err == nil {
			t.Fatalf("accepted invalid arguments %v", args)
		}
	}
	if strings.Contains(safe("hello\x1b[31m\x07"), "\x1b") {
		t.Fatal("terminal escape leaked")
	}
}
