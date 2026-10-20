package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

const help = `wa — read the WhatsApp desktop app's local data

Usage:
  wa status                       Show database paths and archive counts
  wa chats [name-or-phone]         List recent conversations
  wa contacts [name-or-phone]      Find locally saved contacts
  wa read <chat>                   Read a conversation's latest messages
  wa search <text>                 Search message text across conversations
  wa context <message-id>          Show messages around a result
  wa media <message-id>            Locate an attachment already on this Mac

Options (before or after the command):
  --json                          Print structured JSON
  --limit N                       Maximum results (default 30, up to 10000)
  --offset N                      Skip results for pagination
  --chat <name|phone|jid|id>       Restrict search/context/media to a chat
  --after <date>                  Include messages on/after this ISO date
  --before <date>                 Include messages before this ISO date
  --oldest                        Start at the oldest messages
  --all                           Include hidden chats in the chats list
  --around N                      Context messages on either side (default 5)
  --store <directory>             Override WhatsApp's shared storage directory
  --db <ChatStorage.sqlite>       Read a specific database (contacts beside it)
  --help                          Show this help
  --version                       Show version

Examples:
  wa contacts Alice
  wa read "Alice" --limit 50
  wa search "flight" --chat "Alice" --after 2026-01-01
  wa context 12345 --around 8
  wa chats --json

The installed WhatsApp app handles all syncing. This tool opens local databases
read-only. It never pairs, connects to WhatsApp, or sends messages/read receipts.
`

type options struct {
	store, db, chat, after, before   string
	json, oldest, all, help, version bool
	limit, offset, around            int
}

func parse(args []string, errOut io.Writer) (options, []string, error) {
	var o options
	f := flag.NewFlagSet("wa", flag.ContinueOnError)
	f.SetOutput(errOut)
	f.StringVar(&o.store, "store", os.Getenv("WA_STORE"), "")
	f.StringVar(&o.db, "db", os.Getenv("WA_DB"), "")
	f.StringVar(&o.chat, "chat", "", "")
	f.StringVar(&o.after, "after", "", "")
	f.StringVar(&o.before, "before", "", "")
	f.BoolVar(&o.json, "json", false, "")
	f.BoolVar(&o.oldest, "oldest", false, "")
	f.BoolVar(&o.all, "all", false, "")
	f.BoolVar(&o.help, "help", false, "")
	f.BoolVar(&o.help, "h", false, "")
	f.BoolVar(&o.version, "version", false, "")
	f.IntVar(&o.limit, "limit", 30, "")
	f.IntVar(&o.offset, "offset", 0, "")
	f.IntVar(&o.around, "around", 5, "")
	f.Usage = func() { fmt.Fprint(errOut, help) }
	// Standard flag stops at the first positional. Hoist flags so natural command
	// syntax such as `wa read Alice --limit 20` works as well as global options.
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		s := args[i]
		if s == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(s, "-") || s == "-" {
			positionals = append(positionals, s)
			continue
		}
		name := strings.TrimLeft(strings.SplitN(s, "=", 2)[0], "-")
		item := f.Lookup(name)
		if item == nil {
			return o, nil, fmt.Errorf("unknown option %s; use 'wa --help'", s)
		}
		flags = append(flags, s)
		if !strings.Contains(s, "=") {
			b, boolean := item.Value.(interface{ IsBoolFlag() bool })
			if !boolean || !b.IsBoolFlag() {
				i++
				if i >= len(args) {
					return o, nil, fmt.Errorf("%s requires a value", s)
				}
				flags = append(flags, args[i])
			}
		}
	}
	if err := f.Parse(flags); err != nil {
		return o, nil, err
	}
	if o.limit < 1 || o.limit > 10000 {
		return o, nil, errors.New("--limit must be between 1 and 10000")
	}
	if o.offset < 0 {
		return o, nil, errors.New("--offset must be nonnegative")
	}
	if o.around < 0 || o.around > 1000 {
		return o, nil, errors.New("--around must be between 0 and 1000")
	}
	return o, positionals, nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "wa:", safe(err.Error()))
		os.Exit(1)
	}
}

func run(args []string, out, errOut io.Writer) error {
	o, pos, err := parse(args, errOut)
	if err != nil {
		return err
	}
	if o.version {
		fmt.Fprintln(out, "wa 0.1.0 (local WhatsApp desktop reader)")
		return nil
	}
	if o.help || len(pos) == 0 || pos[0] == "help" {
		fmt.Fprint(out, help)
		return nil
	}
	command := pos[0]
	terms := pos[1:]
	switch command {
	case "status", "chats", "contacts", "read", "search", "context", "media":
	default:
		return fmt.Errorf("unknown command %q; use 'wa --help'", command)
	}
	if (command == "read" || command == "search" || command == "context" || command == "media") && len(terms) == 0 {
		return fmt.Errorf("%s needs an argument; use 'wa --help'", command)
	}
	if command == "status" && len(terms) != 0 {
		return errors.New("status takes no arguments")
	}
	if (command == "context" || command == "media") && len(terms) != 1 {
		return fmt.Errorf("%s expects one message ID", command)
	}
	if o.store != "" && o.db != "" {
		return errors.New("choose either --store or --db")
	}
	if o.db == "" {
		if o.store == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			o.store = filepath.Join(home, "Library", "Group Containers", "group.net.whatsapp.WhatsApp.shared")
		}
		o.db = filepath.Join(o.store, "ChatStorage.sqlite")
	}
	o.db, err = filepath.Abs(o.db)
	if err != nil {
		return err
	}
	a, err := openArchive(o.db)
	if err != nil {
		return fmt.Errorf("%w\nUse --store or --db for a different location. If macOS denies access, allow the terminal/Codex app access in Privacy & Security > Full Disk Access", err)
	}
	defer a.db.Close()
	for _, warning := range a.warnings {
		fmt.Fprintln(errOut, "wa:", warning)
	}
	query := strings.Join(terms, " ")
	switch command {
	case "status":
		var count int64
		var earliest, latest float64
		if err := a.db.QueryRow(`SELECT COUNT(*),COALESCE(MIN(ZMESSAGEDATE),0),COALESCE(MAX(ZMESSAGEDATE),0) FROM ZWAMESSAGE`).Scan(&count, &earliest, &latest); err != nil {
			return err
		}
		data := struct {
			Database         string   `json:"database"`
			ContactsDatabase string   `json:"contacts_database"`
			Messages         int64    `json:"messages"`
			Chats            int      `json:"chats"`
			Contacts         int      `json:"contacts"`
			Earliest         string   `json:"earliest"`
			Latest           string   `json:"latest"`
			ReadOnly         bool     `json:"read_only"`
			Warnings         []string `json:"warnings"`
		}{a.path, filepath.Join(a.root, "ContactsV2.sqlite"), count, len(a.chats), len(a.contacts), "", "", true, a.warnings}
		if count > 0 {
			data.Earliest = appleTime(earliest)
			data.Latest = appleTime(latest)
		}
		if data.Warnings == nil {
			data.Warnings = []string{}
		}
		if o.json {
			return jsonOut(out, data)
		}
		fmt.Fprintf(out, "Database:  %s\nContacts:  %s\nMode:      read-only, local app database\nMessages:  %d\nChats:     %d\nContacts:  %d\nEarliest:  %s\nLatest:    %s\n", data.Database, data.ContactsDatabase, count, data.Chats, data.Contacts, data.Earliest, data.Latest)
	case "chats":
		matches := []Chat{}
		for _, c := range a.chats {
			if (!c.Hidden || o.all) && a.chatMatches(c, query) {
				matches = append(matches, c)
			}
		}
		matches = page(matches, o.offset, o.limit)
		if o.json {
			return jsonOut(out, matches)
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tLAST MESSAGE\tNAME\tJID\tPREVIEW")
		for _, c := range matches {
			name := c.Name
			if c.Archived {
				name += " [archived]"
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", c.ID, displayTime(c.LastMessageTime), oneLine(name, 40), safe(c.JID), oneLine(c.Preview, 65))
		}
		w.Flush()
	case "contacts":
		matches := []Contact{}
		for _, c := range a.contacts {
			if contains(c.Name, query) || contains(c.Phone, query) || contains(c.JID, query) || contains(c.LID, query) || (isPhone(query) && strings.Contains(phoneDigits(c.Phone), phoneDigits(query))) {
				matches = append(matches, c)
			}
		}
		matches = page(matches, o.offset, o.limit)
		if o.json {
			return jsonOut(out, matches)
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tPHONE\tJID")
		for _, c := range matches {
			fmt.Fprintf(w, "%s\t%s\t%s\n", oneLine(c.Name, 50), safe(c.Phone), safe(c.JID))
		}
		w.Flush()
	case "read", "search":
		where := "1=1"
		var values []any
		chat := o.chat
		if command == "read" {
			if chat != "" {
				return errors.New("read takes the chat as its argument; omit --chat")
			}
			chat = query
		}
		if chat != "" {
			c, err := a.resolveChat(chat)
			if err != nil {
				return err
			}
			where += " AND m.ZCHATSESSION=?"
			values = append(values, c.ID)
		}
		if command == "search" {
			where += " AND wa_contains(COALESCE(m.ZTEXT,''),?)"
			values = append(values, query)
		}
		after, err := dateBound(o.after)
		if err != nil {
			return err
		}
		before, err := dateBound(o.before)
		if err != nil {
			return err
		}
		if o.after != "" {
			where += " AND m.ZMESSAGEDATE>=?"
			values = append(values, after)
		}
		if o.before != "" {
			where += " AND m.ZMESSAGEDATE<?"
			values = append(values, before)
		}
		if o.after != "" && o.before != "" && after >= before {
			return errors.New("--after must precede --before")
		}
		order := "m.ZMESSAGEDATE DESC,m.Z_PK DESC"
		if o.oldest {
			order = "m.ZMESSAGEDATE ASC,m.Z_PK ASC"
		}
		messages, err := a.messages(where, order, o.limit, o.offset, values...)
		if err != nil {
			return err
		}
		if command == "read" {
			chronological(messages)
		}
		if o.json {
			return jsonOut(out, messages)
		}
		printMessages(out, messages, command == "search")
	case "context":
		anchor, err := a.findMessage(query, o.chat)
		if err != nil {
			return err
		}
		before, err := a.messages("m.ZCHATSESSION=? AND (m.ZMESSAGEDATE<? OR (m.ZMESSAGEDATE=? AND m.Z_PK<?))", "m.ZMESSAGEDATE DESC,m.Z_PK DESC", o.around, 0, anchor.ChatID, anchor.date, anchor.date, anchor.ID)
		if err != nil {
			return err
		}
		after, err := a.messages("m.ZCHATSESSION=? AND (m.ZMESSAGEDATE>? OR (m.ZMESSAGEDATE=? AND m.Z_PK>?))", "m.ZMESSAGEDATE ASC,m.Z_PK ASC", o.around, 0, anchor.ChatID, anchor.date, anchor.date, anchor.ID)
		if err != nil {
			return err
		}
		chronological(before)
		if o.json {
			return jsonOut(out, struct {
				Message Message   `json:"message"`
				Before  []Message `json:"before"`
				After   []Message `json:"after"`
			}{anchor, before, after})
		}
		printMessages(out, before, false)
		fmt.Fprintln(out, "── selected message ──")
		printMessages(out, []Message{anchor}, false)
		fmt.Fprintln(out, "──────────────────────")
		printMessages(out, after, false)
	case "media":
		m, err := a.findMessage(query, o.chat)
		if err != nil {
			return err
		}
		if m.MediaPath == "" {
			return fmt.Errorf("message #%d has no local attachment path; the app may not have downloaded it", m.ID)
		}
		path := m.MediaPath
		if !filepath.IsAbs(path) {
			path = filepath.Join(a.root, path)
		}
		info, err := os.Stat(path)
		exists := err == nil && info.Mode().IsRegular()
		if o.json {
			return jsonOut(out, struct {
				ID     int64  `json:"message_id"`
				Path   string `json:"path"`
				Exists bool   `json:"exists"`
			}{m.ID, path, exists})
		}
		fmt.Fprintln(out, safe(path))
		if !exists {
			return errors.New("attachment is not available locally; open the message in WhatsApp to download it")
		}
	}
	return nil
}

func jsonOut(out io.Writer, value any) error {
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	e.SetEscapeHTML(false)
	return e.Encode(value)
}
func page[T any](items []T, offset, limit int) []T {
	if offset >= len(items) {
		return []T{}
	}
	end := offset + limit
	if end > len(items) || end < offset {
		end = len(items)
	}
	return items[offset:end]
}
func dateBound(value string) (float64, error) {
	if value == "" {
		return 0, nil
	}
	date, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		date, err = time.ParseInLocation("2006-01-02", value, time.Local)
	}
	if err != nil {
		return 0, fmt.Errorf("invalid date %q; use YYYY-MM-DD or RFC3339 with a timezone", value)
	}
	return float64(date.Unix()-appleEpoch) + float64(date.Nanosecond())/1e9, nil
}
func displayTime(value string) string {
	date, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return date.Local().Format("2006-01-02 15:04")
}
func safe(value string) string {
	return strings.Map(func(r rune) rune {
		if (r < 32 && r != '\n' && r != '\t') || r == 127 || (r >= 0x80 && r <= 0x9f) {
			return '�'
		}
		return r
	}, value)
}
func oneLine(value string, limit int) string {
	value = strings.Join(strings.Fields(safe(value)), " ")
	r := []rune(value)
	if len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return value
}
func printMessages(out io.Writer, messages []Message, showChat bool) {
	if len(messages) == 0 {
		fmt.Fprintln(out, "No messages.")
		return
	}
	for _, m := range messages {
		prefix := ""
		if showChat {
			prefix = " [" + safe(m.ChatName) + "]"
		}
		fmt.Fprintf(out, "[%s]%s %s  #%s\n", displayTime(m.Timestamp), prefix, safe(m.Sender), strconv.FormatInt(m.ID, 10))
		text := m.Text
		if text == "" {
			if m.MediaTitle != "" {
				text = m.MediaTitle
			} else {
				text = fmt.Sprintf("[non-text message, type %d]", m.Type)
			}
		}
		fmt.Fprintln(out, safe(text))
		if m.MediaPath != "" {
			fmt.Fprintf(out, "  attachment: wa media %d\n", m.ID)
		}
		fmt.Fprintln(out)
	}
}
