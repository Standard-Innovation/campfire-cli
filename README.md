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
| **Write token** | The same change, with bot and room endpoints | Make and change rooms; as an administrator, make, rekey and delete bots |

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
campfire room create --name Deploys --member 12  # closed room with you and user or bot 12
campfire room create --name News --open          # open room: everyone
campfire room update 10 --member 2 --member 12   # replace a closed room's members; asks first
campfire bot list                                # bots (administrators)
campfire bot create --name "Deploy Bot"          # make a bot; shows its key once
campfire bot rekey 12                            # new key for bot 12; asks first, shows it once
campfire bot delete 12                           # asks first; --yes skips the question
campfire auth status                             # who am I?
campfire doctor                                  # check setup
```

`--room` takes an ID, a room or message URL, a room name, or, for a direct message, the other person's name. Bot keys need an ID or URL.

### Bots and rooms

Making rooms and bots needs a **write** personal access token. Bots also need the token's owner to be an administrator. A `403` means the token is read-only, or its owner can't do that.

- **A bot's key is shown once**, by `bot create` and `bot rekey`. Store it straight into a secret manager in one command: `--quiet` prints the key alone, so it can be piped to a tool that reads stdin and never appears on your screen or in your scrollback, for example `campfire bot create --name "Deploy Bot" --quiet | gh secret set DEPLOY_BOT_KEY`. The CLI never saves it to a profile or config, and `--verbose` never logs it.
- **Piped JSON output includes the key.** When output is not a terminal, or with `--json`, the envelope carries `bot_key`, so anything reading that output sees it. `--agent` refuses `bot create` and `bot rekey` unless you pass `--quiet` (or `--jq`).
- `bot rekey` stops the current key at once and is not retried. If it fails, rekey again. `bot rekey`, `bot delete` and `room update` ask first; pass `--yes` to skip the question, which is required with `--agent` or without a terminal.
- **Tokens cannot set a bot's webhook.** A webhook makes the server call any address it can reach, so a leaked token must not be able to set one. Add webhooks in the web UI.
- `room update` takes a room ID, URL or name. `--member` is the room's whole membership: list everyone who should stay. Campfire always keeps you in the room. Without `--name` the room keeps its name. Open rooms hold everyone, so `room create --open` takes no `--member`, and `room update` refuses an open room: change it in the web UI.

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
- With a write token: `GET`/`POST /account/bots.json`, `PUT /account/bots/:id/key.json` and `DELETE /account/bots/:id.json` for administrators, and `POST /rooms/closeds.json`, `POST /rooms/opens.json` and `PUT /rooms/closeds/:id.json`. A token can't set a bot's `webhook_url`.

## Development

```sh
make build      # bin/campfire
make test-unit  # unit tests against a fake Campfire
make check      # fmt, vet, lint, tidy, race tests
```

See [AGENTS.md](AGENTS.md) for conventions.

## License

[MIT](MIT-LICENSE). Includes code adapted from basecamp/fizzy-cli, © Rob Zolkos and 37signals.
