package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
)

const appleEpoch = 978307200

func init() {
	sql.Register("whatsapp-readonly", &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
		return conn.RegisterFunc("wa_contains", contains, true)
	}})
}

type Contact struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Phone string `json:"phone"`
	JID   string `json:"jid"`
	LID   string `json:"lid,omitempty"`
}

type Chat struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	JID             string `json:"jid"`
	Phone           string `json:"phone,omitempty"`
	LastMessageTime string `json:"last_message_time,omitempty"`
	Preview         string `json:"preview"`
	Archived        bool   `json:"archived"`
	Hidden          bool   `json:"hidden"`
	Unread          int64  `json:"unread"`
}

type Message struct {
	ID         int64  `json:"id"`
	StanzaID   string `json:"stanza_id"`
	ChatID     int64  `json:"chat_id"`
	ChatName   string `json:"chat_name"`
	ChatJID    string `json:"chat_jid"`
	Timestamp  string `json:"timestamp"`
	Sender     string `json:"sender"`
	SenderJID  string `json:"sender_jid"`
	FromMe     bool   `json:"from_me"`
	Text       string `json:"text"`
	Type       int64  `json:"type"`
	MediaPath  string `json:"media_path,omitempty"`
	MediaTitle string `json:"media_title,omitempty"`
	date       float64
}

type Archive struct {
	db       *sql.DB
	path     string
	root     string
	contacts []Contact
	byJID    map[string]Contact
	chats    []Chat
	warnings []string
}

// mode=ro refuses to create or modify the source; query_only also rejects writes
// through this connection. We intentionally leave immutable off to see live WAL data.
func openReadOnly(path string) (*sql.DB, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a database file: %s", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("_query_only", "true")
	q.Set("_busy_timeout", "3000")
	u.RawQuery = q.Encode()
	db, err := sql.Open("whatsapp-readonly", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening %s read-only: %w", path, err)
	}
	return db, nil
}

func openArchive(path string) (*Archive, error) {
	db, err := openReadOnly(path)
	if err != nil {
		return nil, err
	}
	a := &Archive{db: db, path: path, root: filepath.Dir(path), byJID: map[string]Contact{}, contacts: []Contact{}, chats: []Chat{}}
	if err := a.validate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := a.loadContacts(); err != nil {
		a.warnings = append(a.warnings, "ContactsV2.sqlite unavailable: "+err.Error())
	}
	if err := a.loadChats(); err != nil {
		db.Close()
		return nil, err
	}
	return a, nil
}

func (a *Archive) validate() error {
	// Validate every table/column used by message retrieval before issuing queries.
	rows, err := a.db.Query(`SELECT m.Z_PK,m.ZSTANZAID,m.ZCHATSESSION,m.ZMESSAGEDATE,m.ZISFROMME,m.ZTEXT,m.ZMESSAGETYPE,m.ZPUSHNAME,m.ZFROMJID,
	 c.Z_PK,c.ZCONTACTJID,c.ZPARTNERNAME,c.ZLASTMESSAGEDATE,c.ZLASTMESSAGETEXT,c.ZARCHIVED,c.ZHIDDEN,c.ZUNREADCOUNT,
	 g.ZMEMBERJID,g.ZCONTACTNAME,i.ZMEDIALOCALPATH,i.ZTITLE
	 FROM ZWAMESSAGE m LEFT JOIN ZWACHATSESSION c ON c.Z_PK=m.ZCHATSESSION
	 LEFT JOIN ZWAGROUPMEMBER g ON g.Z_PK=m.ZGROUPMEMBER LEFT JOIN ZWAMEDIAITEM i ON i.Z_PK=m.ZMEDIAITEM LIMIT 0`)
	if err != nil {
		return fmt.Errorf("unsupported WhatsApp desktop database schema: %w", err)
	}
	return rows.Close()
}

func (a *Archive) loadContacts() error {
	db, err := openReadOnly(filepath.Join(a.root, "ContactsV2.sqlite"))
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT Z_PK, COALESCE(NULLIF(ZFULLNAME,''),NULLIF(ZGIVENNAME,''),NULLIF(ZBUSINESSNAME,''),''),
	 COALESCE(NULLIF(ZPHONENUMBER,''),ZLOCALIZEDPHONENUMBER,''),COALESCE(ZWHATSAPPID,''),COALESCE(ZLID,'') FROM ZWAADDRESSBOOKCONTACT ORDER BY Z_PK`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.ID, &c.Name, &c.Phone, &c.JID, &c.LID); err != nil {
			return err
		}
		if c.Phone == "" && strings.HasSuffix(c.JID, "@s.whatsapp.net") {
			c.Phone = strings.Split(c.JID, "@")[0]
		}
		if c.Name == "" {
			c.Name = c.Phone
		}
		if c.Name == "" {
			c.Name = c.JID
		}
		a.contacts = append(a.contacts, c)
		if c.JID != "" {
			a.byJID[c.JID] = c
		}
		if c.LID != "" {
			a.byJID[c.LID] = c
		}
	}
	return rows.Err()
}

func (a *Archive) loadChats() error {
	rows, err := a.db.Query(`SELECT Z_PK,COALESCE(ZCONTACTJID,''),COALESCE(ZPARTNERNAME,''),COALESCE(ZLASTMESSAGEDATE,0),
	 COALESCE(ZLASTMESSAGETEXT,''),COALESCE(ZARCHIVED,0),COALESCE(ZHIDDEN,0),COALESCE(ZUNREADCOUNT,0)
	 FROM ZWACHATSESSION ORDER BY ZLASTMESSAGEDATE DESC,Z_PK DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c Chat
		var date float64
		if err := rows.Scan(&c.ID, &c.JID, &c.Name, &date, &c.Preview, &c.Archived, &c.Hidden, &c.Unread); err != nil {
			return err
		}
		if contact, ok := a.byJID[c.JID]; ok {
			c.Phone = contact.Phone
			if c.Name == "" {
				c.Name = contact.Name
			}
		}
		if c.Phone == "" && strings.HasSuffix(c.JID, "@s.whatsapp.net") {
			c.Phone = strings.Split(c.JID, "@")[0]
		}
		if c.Name == "" {
			c.Name = c.JID
		}
		if date != 0 {
			c.LastMessageTime = appleTime(date)
		}
		a.chats = append(a.chats, c)
	}
	return rows.Err()
}

func appleTime(date float64) string {
	seconds := int64(date)
	return time.Unix(seconds+appleEpoch, int64((date-float64(seconds))*1e9)).UTC().Format(time.RFC3339Nano)
}

func contains(value, query string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(query))
}

func phoneDigits(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (a *Archive) resolveChat(selector string) (Chat, error) {
	// A numeric database ID is unambiguous, even when names are duplicated.
	for _, c := range a.chats {
		if selector == strconv.FormatInt(c.ID, 10) {
			return c, nil
		}
	}
	var exact, partial []Chat
	for _, c := range a.chats {
		contactName := a.byJID[c.JID].Name
		phoneMatch := isPhone(selector) && phoneDigits(selector) == phoneDigits(c.Phone)
		if strings.EqualFold(selector, c.JID) || strings.EqualFold(selector, c.Name) || (contactName != "" && strings.EqualFold(selector, contactName)) || phoneMatch {
			exact = append(exact, c)
		} else if a.chatMatches(c, selector) {
			partial = append(partial, c)
		}
	}
	candidates := exact
	if len(candidates) == 0 {
		candidates = partial
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if len(candidates) == 0 {
		return Chat{}, fmt.Errorf("no chat matches %q; use 'wa chats' to find a chat", selector)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%q matches %d chats; use a chat ID or full JID:", selector, len(candidates))
	for i, c := range candidates {
		if i == 10 {
			b.WriteString("\n  ...")
			break
		}
		fmt.Fprintf(&b, "\n  %d  %s  %s", c.ID, c.Name, c.JID)
	}
	return Chat{}, fmt.Errorf("%s", b.String())
}

func (a *Archive) chatMatches(c Chat, query string) bool {
	return contains(c.Name, query) || contains(c.JID, query) || contains(c.Phone, query) ||
		contains(a.byJID[c.JID].Name, query) || (isPhone(query) && strings.Contains(phoneDigits(c.Phone), phoneDigits(query)))
}

func isPhone(value string) bool {
	if phoneDigits(value) == "" {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && !strings.ContainsRune("+()- .", r) {
			return false
		}
	}
	return true
}

const messageSelect = `SELECT m.Z_PK,COALESCE(m.ZSTANZAID,''),COALESCE(m.ZCHATSESSION,0),COALESCE(c.ZPARTNERNAME,''),COALESCE(c.ZCONTACTJID,''),
 COALESCE(m.ZMESSAGEDATE,0),COALESCE(m.ZISFROMME,0),COALESCE(m.ZTEXT,''),COALESCE(m.ZMESSAGETYPE,0),
 COALESCE(NULLIF(g.ZMEMBERJID,''),NULLIF(m.ZFROMJID,''),c.ZCONTACTJID,''),
 COALESCE(NULLIF(g.ZCONTACTNAME,''),m.ZPUSHNAME,''),COALESCE(i.ZMEDIALOCALPATH,''),COALESCE(i.ZTITLE,'')
 FROM ZWAMESSAGE m LEFT JOIN ZWACHATSESSION c ON c.Z_PK=m.ZCHATSESSION
 LEFT JOIN ZWAGROUPMEMBER g ON g.Z_PK=m.ZGROUPMEMBER LEFT JOIN ZWAMEDIAITEM i ON i.Z_PK=m.ZMEDIAITEM `

func (a *Archive) messages(where, order string, limit, offset int, args ...any) ([]Message, error) {
	query := messageSelect + " WHERE " + where + " ORDER BY " + order + " LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err := a.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.StanzaID, &m.ChatID, &m.ChatName, &m.ChatJID, &m.date, &m.FromMe, &m.Text, &m.Type, &m.SenderJID, &m.Sender, &m.MediaPath, &m.MediaTitle); err != nil {
			return nil, err
		}
		m.Timestamp = appleTime(m.date)
		if m.FromMe {
			m.Sender = "Me"
		} else if c, ok := a.byJID[m.SenderJID]; ok {
			m.Sender = c.Name
		}
		if m.Sender == "" {
			m.Sender = m.SenderJID
		}
		if m.ChatName == "" {
			if c, ok := a.byJID[m.ChatJID]; ok {
				m.ChatName = c.Name
			} else {
				m.ChatName = m.ChatJID
			}
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func (a *Archive) findMessage(ref, chat string) (Message, error) {
	where := "m.ZSTANZAID=?"
	args := []any{ref}
	if id, err := strconv.ParseInt(strings.TrimPrefix(ref, "#"), 10, 64); err == nil {
		where = "m.Z_PK=?"
		args = []any{id}
	}
	if chat != "" {
		c, err := a.resolveChat(chat)
		if err != nil {
			return Message{}, err
		}
		where += " AND m.ZCHATSESSION=?"
		args = append(args, c.ID)
	}
	messages, err := a.messages(where, "m.ZMESSAGEDATE,m.Z_PK", 2, 0, args...)
	if err != nil {
		return Message{}, err
	}
	if len(messages) == 0 {
		return Message{}, fmt.Errorf("message %q not found", ref)
	}
	if len(messages) > 1 {
		return Message{}, fmt.Errorf("message ID %q appears in multiple chats; add --chat", ref)
	}
	return messages[0], nil
}

func chronological(messages []Message) {
	sort.Slice(messages, func(i, j int) bool {
		if messages[i].date == messages[j].date {
			return messages[i].ID < messages[j].ID
		}
		return messages[i].date < messages[j].date
	})
}
