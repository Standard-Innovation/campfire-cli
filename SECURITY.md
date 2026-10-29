# Security Policy

## Reporting a Vulnerability

Please report security vulnerabilities privately through GitHub's
[private vulnerability reporting](https://github.com/Standard-Innovation/campfire-cli/security/advisories/new).

Do **NOT** open public GitHub issues for security vulnerabilities.

## Credential Storage

The Campfire CLI stores personal access tokens and bot keys securely using your operating system's native credential storage:

| Platform | Storage |
|----------|---------|
| macOS | Keychain |
| Windows | Credential Manager |
| Linux | Secret Service (GNOME Keyring, KWallet) |

### File-based Fallback

If system keyring is unavailable (headless servers, containers), set:

```bash
export CAMPFIRE_NO_KEYRING=1
```

Credentials will be stored as individual files in `~/.config/campfire/credentials/`, each created with `0600` permissions.

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest  | Yes       |
| < Latest | No       |

We only provide security fixes for the latest release. Users should upgrade promptly.

## Credential Handling

- Credentials can be piped to `campfire auth login` on stdin, which keeps them out of shell history and process listings.
- Bot keys travel in Campfire's URL path. The CLI masks them in `--verbose` logs and never echoes paging URLs in its output.
- Paging links are only followed on the configured Campfire origin, so a credential is never sent to another host.
- Plain `http://` Campfire URLs are refused except for localhost.
