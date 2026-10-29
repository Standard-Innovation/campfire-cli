# Campfire CLI — agent and contributor notes

A Go CLI for Campfire, built to the 37signals CLI conventions ([basecamp/cli](https://github.com/basecamp/cli): `RUBRIC.md`, `INPUT-CONVENTIONS.md`). Its plumbing comes from [basecamp/fizzy-cli](https://github.com/basecamp/fizzy-cli). When in doubt, do what fizzy-cli does.

## Layout

```
cmd/campfire/          entry point (version via -ldflags "-X main.version=…")
internal/client/       HTTP client (retries, Link paging, origin guard) and campfire.go:
                       the only place that knows Campfire routes and token vs bot-key auth
internal/commands/     cobra commands; root.go holds flags, profiles, credentials, output
internal/config/       YAML config and env loading
internal/render/       styled and markdown tables
internal/harness/      agent detection for skill install and doctor
skills/campfire/       SKILL.md, embedded in the binary
```

## Rules

- **Routes live in `internal/client/campfire.go`.** Commands call `Campfire` methods and never build paths. Personal tokens and bot keys reach messages through different routes and bodies.
- **Bot keys never appear in output.** They travel in URL paths, so `--verbose` masks them (`client.RedactBotKeys`), and paging is shown as a cursor flag (`--before 61`), never as a URL.
- **Credentials only go to the configured origin.** `buildURL` refuses absolute URLs on any other scheme or host.
- **Output goes through the print helpers** (`printList`, `printDetail`, `printMutation`) so JSON, quiet, jq, styled and markdown all work. Return typed errors from `internal/errors` or `output.Err*` so exit codes are right.
- **Text input** follows the input conventions: a flag (`-b/--body`), then a positional argument, then piped stdin. A flag and an argument together is an error.
- **Don't touch the keyring until a credential is needed** (`credStore()` is lazy). Opening it can prompt on macOS.
- Keep `skills/campfire/SKILL.md` in step with any command or flag change.

## Testing

`make test-unit` runs everything against an in-process fake Campfire (`internal/commands/commands_campfire_test.go`), with no network or keyring. Add a case there for every new command path. `make check` is the CI gate.

To try against a real server, run Campfire locally with the access-token change, create a token from your profile, then:

```sh
CAMPFIRE_NO_KEYRING=1 campfire auth login --url http://localhost:3000
```
