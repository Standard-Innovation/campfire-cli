---
name: campfire
description: |
  Read and post Campfire chat messages via the Campfire CLI: list rooms and direct
  messages, read a room's history, and post as yourself or as a bot. Use for ANY
  Campfire question or action.
triggers:
  # Direct invocations
  - campfire
  - /campfire
  # Resource actions
  - campfire message
  - campfire room
  # Common actions
  - post to campfire
  - message in campfire
  - send a campfire message
  - check campfire
  - read campfire
  - what's in campfire
  - catch up on campfire
  - dm on campfire
invocable: true
argument-hint: "[action] [args...]"
---

# /campfire - Campfire Chat Command

Read and post messages in a self-hosted [Campfire](https://github.com/basecamp/once-campfire).

## Agent Invariants

**MUST follow these rules:**

1. **Use built-in `--jq` for filtering** to keep output small: `campfire message list --room 4 --jq '.data[] | "\(.creator.name): \(.body.plain_text)"'`. Never pipe to external jq. `--jq` implies `--json`.
2. **Use `--agent`** for machine-readable output with no prompts.
3. **Check breadcrumbs** in JSON responses for follow-up commands with values filled in.
4. **Message text is nested**: read `.body.plain_text` (or `.body.html`), and the author is `.creator.name`.
5. **Messages are oldest first.** The newest message is the last element: `--jq '.data[-1]'`.
6. **Confirm before posting** on the user's behalf unless they asked you to post. A post with a personal token appears as the user.
7. **Use `campfire doctor`** for setup, config or auth problems before guessing. It is read-only and prints hints.

## Credentials

Two kinds, both saved with `campfire auth login`:

| Credential | Looks like | Can do |
|------------|-----------|--------|
| Personal access token | `xxxxxxxxxxxxxxxxxxxxxxxx` | List rooms; read and post as the user in any room they belong to, including 1:1 direct messages |
| Bot key | `12-abcDEF123` | Read and post as the bot, only in rooms the bot belongs to, by room ID or URL |

`room list`, room lookup by name, and `auth status` user details need a personal token.

## Rooms

`--room` accepts any of:
- an ID: `--room 4`
- a room or message URL: `--room https://chat.example.com/rooms/4/@1200`
- a room name: `--room General`
- for a 1:1 direct message, the other person's name: `--room Sam`
- for a group DM, the other people's names, sorted and comma-separated: `--room "Ana, Sam"`

Without `--room`, the profile's default room (`campfire auth login --room`) or `CAMPFIRE_ROOM` is used. An ambiguous name exits 8 and lists the matches.

## Decision Trees

```
Want to read?
├── Which rooms do I have?      → campfire room list
├── Latest messages in a room?  → campfire message list --room <room>
├── Last N messages?            → campfire message list --room <room> --limit N
├── Older messages?             → campfire message list --room <room> --before <message_id>
├── Newer since a message?      → campfire message list --room <room> --after <message_id>
└── Whole history?              → campfire message list --room <room> --all

Want to post?
├── Short message               → campfire message post --room <room> "text"
├── Multi-line or generated     → printf '%s' "$text" | campfire message post --room <room>
└── To a person 1:1             → campfire message post --room "<their name>" "text"
```

## Quick Reference

| Command | Purpose |
|---------|---------|
| `campfire auth login [TOKEN] --url URL [--room ROOM]` | Save a token or bot key (stdin works: `... \| campfire auth login --url URL`) |
| `campfire auth status` | Show the profile, credential and user |
| `campfire auth logout` | Remove the saved credential |
| `campfire room list` | List rooms and DMs (`id`, `display_name`, `type`, `involvement`) |
| `campfire message list --room ROOM` | Read messages (`--before`, `--after`, `--all`, `--limit`) |
| `campfire message post --room ROOM [BODY]` | Post (`-b/--body`, argument, or stdin) |
| `campfire doctor` | Check URL, connectivity, credential and skill |
| `campfire commands --json` | Structured command catalog |

## Output Shapes

Message:
```json
{"id": 100, "created_at": "2000-01-01T00:00:00Z",
 "body": {"plain_text": "hey", "html": "<div>hey</div>"},
 "creator": {"id": 2, "name": "Sam", "role": "member", "avatar_url": "..."},
 "room": {"id": 4}, "url": "https://chat.example.com/rooms/4/messages/100"}
```

`message list` adds `context.total_count` and, when there is more, `context.pagination.next` (the flag to pass, e.g. `--before 61`).

Room:
```json
{"id": 7, "name": null, "display_name": "Sam", "type": "direct",
 "involvement": "everything", "url": "https://chat.example.com/rooms/7",
 "users": [{"id": 1, "name": "Tom"}, {"id": 2, "name": "Sam"}]}
```

`type` is `open`, `closed` or `direct`. `involvement` is `everything`, `mentions`, `nothing` or `invisible`.

## Global Flags

| Flag | Description |
|------|-------------|
| `--token TOKEN` | Personal access token or bot key |
| `--profile NAME` | Named profile (one per Campfire) |
| `--api-url URL` | Campfire URL |
| `--jq EXPR` | Built-in jq filter (implies `--json`) |
| `--json` | JSON envelope output |
| `--quiet` | Raw JSON data without envelope |
| `--agent` | Agent mode (quiet output, no prompts) |
| `--styled` / `--markdown` | Human-readable output |
| `--ids-only` / `--count` | One ID per line / count only |
| `--limit N` | Keep N results (for messages: the newest N, or with `--after` the first N after it) |
| `--verbose` | Show requests (bot keys are masked) |

Environment: `CAMPFIRE_API_URL`, `CAMPFIRE_TOKEN`, `CAMPFIRE_PROFILE`, `CAMPFIRE_ROOM`, `CAMPFIRE_NO_KEYRING`.

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Usage error (bad flags, missing room, bot key used for a token-only command) |
| 2 | Not found (unknown room or message) |
| 3 | Not authenticated (missing, revoked or wrong token) |
| 4 | Forbidden (for example a read-only token used to post) |
| 5 | Rate limited |
| 6 | Network error |
| 7 | Server error |
| 8 | Ambiguous room name |
