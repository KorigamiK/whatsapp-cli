# WhatsApp local reader

A small Go CLI that reads the **WhatsApp desktop app already installed on your Mac**. The app remains responsible for downloading and syncing messages. This tool reads its SQLite databases directly and works without an internet connection.

No pairing, QR codes, WhatsApp protocol client, MCP server, web server, telemetry, or AI service. There are no send, delete, or mark-as-read commands.

## Install

Download a macOS archive from [the latest release](https://github.com/KorigamiK/whatsapp-cli/releases/latest):

- `wa_0.1.0_darwin_arm64.tar.gz` for Apple Silicon Macs.
- `wa_0.1.0_darwin_amd64.tar.gz` for Intel Macs.

Extract the archive, then install the executable:

```sh
tar -xzf wa_0.1.0_darwin_arm64.tar.gz
mkdir -p "$HOME/.local/bin"
cp wa "$HOME/.local/bin/wa"
wa --version
```

Use the `amd64` filename instead for an Intel Mac. Ensure `~/.local/bin` is in your shell's PATH. Release archives include the README, MIT license, and agent skill; `SHA256SUMS` provides archive checksums. The binaries are not Apple Developer ID signed or notarized.

## Use it

The built executable is `bin/wa`:

```sh
./bin/wa status
./bin/wa chats
./bin/wa contacts Alice
./bin/wa read "Alice" --limit 50
./bin/wa search "flight"
./bin/wa search "flight" --chat "Alice" --after 2026-01-01
./bin/wa context 12345 --around 5
./bin/wa media 12345
```

Chat selectors accept a displayed chat ID, exact name, phone number, full WhatsApp JID, or a unique partial name. If a name matches several chats, the CLI lists candidates and asks you to use an ID or JID. Phone-number lookup also uses the app's contact mapping for `@lid` identifiers.

`read` fetches the latest 30 messages by default and displays that page in chronological order. Use `--oldest` to start at the beginning, `--limit` to adjust the page size, and `--offset` to retrieve another page. `search` returns the most recent matches first and supports case-insensitive Unicode literal text (quotes, `%`, and `_` are ordinary characters). Hidden chats are omitted from `chats` unless you pass `--all`; archived chats are included and labeled.

Each displayed message has a numeric `#ID`. Pass this ID to `context` or `media`. Context stays within the selected message's conversation, with stable ordering when timestamps are identical. WhatsApp stanza IDs also work; use `--chat` if the same stanza ID occurs in more than one conversation.

Date-only filters use midnight in the Mac's local timezone. `--after` is inclusive; `--before` is exclusive. To make the timezone explicit, use RFC3339 timestamps such as `2026-01-01T00:00:00+05:30`. Human-readable timestamps use the Mac's local timezone; JSON timestamps use UTC.

## JSON and other locations

```sh
./bin/wa chats --json
./bin/wa contacts --json --limit 1000
./bin/wa read "Alice" --json
./bin/wa search "invoice" --json > invoices.json
./bin/wa --store "/path/to/shared-container" status
./bin/wa --db "/path/to/ChatStorage.sqlite" status
```

`--json` emits an array for lists, an object for `status`/`media`, and an object containing `message`, `before`, and `after` for `context`. Errors and optional contacts-database warnings go to stderr; errors return exit code 1. JSON preserves full text and names; terminal output removes control characters. Flags can appear before or after the command. Use `--` before positional text beginning with a dash.

The default store is:

```text
~/Library/Group Containers/group.net.whatsapp.WhatsApp.shared/
```

You can set `WA_STORE` to another shared directory, or `WA_DB` to a particular `ChatStorage.sqlite`. Choose one. `ContactsV2.sqlite` is loaded from the same directory; it includes contacts with no conversation. If it is unavailable, messages and chats remain readable with the names stored in the chat database, and `contacts` returns an empty list with a warning. No WhatsApp authentication/key databases are used.

## Build

```sh
git clone https://github.com/KorigamiK/whatsapp-cli.git
cd whatsapp-cli
go build -trimpath -o bin/wa .
go test ./...
go vet ./...
```

Go 1.22+ and a C compiler are needed **only to build**. The sole library dependency is `github.com/mattn/go-sqlite3`, pinned in `go.mod`/`go.sum`. Its SQLite engine is compiled into the executable. The finished executable needs no Go installation, Python, package manager, or separate SQLite command. The initial build may download the dependency; running the CLI has no network activity.

For a command available anywhere:

```sh
mkdir -p "$HOME/.local/bin"
cp bin/wa "$HOME/.local/bin/wa"
```

Ensure `~/.local/bin` is in your shell's PATH. `wa --help` lists every command and option.

## Agent skill

The reusable agent instructions are in [skills/whatsapp-local/SKILL.md](skills/whatsapp-local/SKILL.md). Install them under `~/.agents/skills`:

```sh
mkdir -p "$HOME/.agents/skills/whatsapp-local"
cp skills/whatsapp-local/SKILL.md "$HOME/.agents/skills/whatsapp-local/SKILL.md"
```

The skill is named `whatsapp-local`. In an agent session that discovers this skill directory, invoke it as `$whatsapp-local` or ask to retrieve local WhatsApp conversations/contacts. It documents query selection, JSON results, pagination, dates, message context, attachments, and read-only troubleshooting. The skill uses the separately installed `wa` executable.

## What is read and what is preserved

The message database is opened with SQLite `mode=ro` and `query_only=true`. The contacts database uses the same settings. The tool does not create indexes, migrate tables, change unread counts, or copy conversations to a separate archive. It reads the current WAL journal so messages recently stored by the app are visible. SQLite can use the app's shared-memory journal coordination file for read locks; SQL writes to the database and WAL are rejected. Tests verify this against a synthetic database while its WAL remains active.

Only history and attachments already stored by the desktop app are available. `media` reports the local attachment path and whether the file exists; it does not download anything. Non-text messages retain their app message-type code and available media metadata. Rich payloads such as reactions, polls, and quoted-message blobs are not decoded in this version.

The repository's tests create synthetic databases in temporary directories. No personal conversations, contacts, attachments, or database exports are included.

The native schema was inspected on WhatsApp for Mac **26.36.74**. Apple/Core Data timestamps are converted from the 2001 epoch. The CLI validates the tables and columns it needs and reports an unsupported-schema error if a future app update changes them. If macOS denies access to the container, give the terminal or application running the CLI Full Disk Access in System Settings → Privacy & Security.

Inspired by the local-retrieval portion of [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp). Its Go bridge pairs a new WhatsApp client and maintains a separate database; this implementation reads the existing Mac app's databases instead. The original project's MIT license is retained in `LICENSE`.
