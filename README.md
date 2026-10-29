# Campfire CLI

Read and post [Campfire](https://github.com/basecamp/once-campfire) messages from the terminal, scripts and AI agents.

```sh
$ campfire message list --room Sam --limit 3
$ campfire message post --room General "Deploy finished"
$ git log -1 --format=%s | campfire message post --room 4
```

An unofficial client, not affiliated with or endorsed by 37signals. It follows the conventions of the 37signals CLIs ([basecamp/cli](https://github.com/basecamp/cli)), and much of its plumbing is adapted from [basecamp/fizzy-cli](https://github.com/basecamp/fizzy-cli). That includes structured JSON output with breadcrumbs, typed exit codes, built-in `--jq`, keyring credential storage, named profiles and an embedded agent skill.

## What your Campfire needs

Campfire is self-hosted, so the CLI's features depend on what your server supports.

| Credential | Server support | What you can do |
|------------|----------------|-----------------|
| **Bot key** | Any current Campfire: upstream's bot API | Read and post as the bot, in rooms the bot belongs to |
| **Personal access token** | Campfire with personal access tokens ([proposed change](#server-side-personal-access-tokens)) | List rooms; read and post as yourself anywhere you're a member, including 1:1 direct messages |

## Install

```sh
go install github.com/Standard-Innovation/campfire-cli/cmd/campfire@latest
```

Or download a binary from [Releases](https://github.com/Standard-Innovation/campfire-cli/releases).

## Log in

With a personal access token (create one from your Campfire profile under **Access tokens**):

```sh
$ op read op://Private/Campfire/token | campfire auth login --url https://chat.example.com
✓ Credential saved for https://chat.example.com
```

With a bot key (an admin can see it under **Bots** in account settings). Pass a room the bot belongs to so it can be checked:

```sh
$ campfire auth login 12-abcDEF123 --url https://chat.example.com --room 4
```

You can pipe the credential on stdin, pass it as an argument, or omit it to be prompted. Piping keeps it out of your shell history. Credentials go in the system keyring, or `~/.config/campfire/credentials/` with `0600` permissions when `CAMPFIRE_NO_KEYRING=1` is set. Each Campfire gets its own profile; switch with `--profile`.

## Use

```sh
campfire room list                               # rooms and DMs (needs a token)
campfire message list --room 4                   # latest page, oldest first
campfire message list --room Sam --limit 10      # your 1:1 with Sam, last 10
campfire message list --room 4 --before 1200     # page back
campfire message list --room 4 --all             # whole history
campfire message post --room General "Hello"     # post
campfire message post --room 4 -b "Hello"        # same, with a flag
campfire auth status                             # who am I?
campfire doctor                                  # check setup
```

`--room` takes an ID, a room or message URL, a room name, or, for a direct message, the other person's name. Bot keys need an ID or URL.

### Output

On a terminal you get tables. When output is piped, or with `--json`, you get the JSON envelope shared by the 37signals CLIs:

```sh
$ campfire message list --room 4 --limit 2 --jq '.data[] | "\(.creator.name): \(.body.plain_text)"'
Sam: Standup in 5
Tom: On my way
```

| Flag | Output |
|------|--------|
| `--json` | `{"ok": true, "data": …, "summary": …, "breadcrumbs": […]}` |
| `--quiet` / `--agent` | Just `data` |
| `--jq EXPR` | Built-in jq over the envelope (no external jq needed) |
| `--ids-only`, `--count` | One ID per line; a count |
| `--styled`, `--markdown` | Human-readable |

Exit codes: 0 ok, 1 usage, 2 not found, 3 auth, 4 forbidden, 5 rate limited, 6 network, 7 server, 8 ambiguous.

### Configuration

Precedence, highest first: flags, environment, profile, `.campfire.yaml` (searched upward from the working directory), `~/.config/campfire/config.yaml`.

| Setting | Flag | Environment | YAML key |
|---------|------|-------------|----------|
| Campfire URL | `--api-url` | `CAMPFIRE_API_URL` | `api_url` |
| Credential | `--token` | `CAMPFIRE_TOKEN` | `token` |
| Default room | per command `--room` | `CAMPFIRE_ROOM` | `room` |
| Profile | `--profile` | `CAMPFIRE_PROFILE` | |

## For agents

```sh
campfire skill install   # installs SKILL.md to ~/.agents/skills/campfire (and links it for Claude Code)
campfire skill           # prints the skill
campfire commands --json # structured command catalog
campfire --agent --help  # structured help for any command
```

## Server-side personal access tokens

Upstream Campfire authenticates bots only. Personal access tokens come from a small change modelled on Fizzy's, which we intend to offer upstream:

- An `AccessToken` model (read or read+write) and a page under your profile to create and revoke tokens. Each token is shown once.
- `Authorization: Bearer` authentication for JSON requests.
- `GET /users/me/profile.json`, `GET /rooms.json`, and `GET`/`POST /rooms/:id/messages.json`. Paging matches the existing bot API: `before`/`after`, `Link`, `X-Total-Count`.

## Development

```sh
make build      # bin/campfire
make test-unit  # unit tests against a fake Campfire
make check      # fmt, vet, lint, tidy, race tests
```

See [AGENTS.md](AGENTS.md) for conventions.

## License

[MIT](MIT-LICENSE). Includes code adapted from basecamp/fizzy-cli, © Rob Zolkos and 37signals.
