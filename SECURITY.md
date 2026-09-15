# Security

## Reporting a vulnerability

Report it privately through GitHub:
[Security → Report a vulnerability](https://github.com/key-arg/statable-cli/security/advisories/new).
If you cannot use GitHub, email **support@statable.com** with "security" in
the subject.

Please do not open a public issue for it. You are welcome to be credited in
the advisory once a fix is released.

A vulnerability in the Statable service rather than in this program goes to the
same address.

## Supported versions

Only the latest release receives fixes. Upgrade before reporting if you can:
`brew upgrade statable`, or `go install github.com/key-arg/statable-cli/cmd/statable@latest`.

| Version | Supported |
|---|---|
| 0.3.x | yes |
| < 0.3 | no |

0.1.0 was built with a Go release that has known vulnerabilities in its TLS
and HTTP code. Do not use it.

## How the key is kept

The API key is the one secret this program handles. Anyone holding it can read
every site it covers, and a key with the manage scopes can change or delete them.

- **Where it is looked for**, in order: `--key`, `STATABLE_API_KEY`, the system
  keyring, a file. `statable auth status` says which one supplied it.
- **The system keyring** is the default store: Keychain on macOS, the Secret
  Service on Linux, Credential Manager on Windows, under the service name
  `statable-cli`.
- **Never silently in plaintext.** If the keyring is unavailable, `auth login`
  fails instead of falling back to a file. A file is written only when you pass
  `--insecure-storage`: `credentials.json`, mode 0600, in a directory forced to
  0700. On Windows those modes are not enforced, and the command says so.
- **Redacted when printed.** `auth status` and `auth login` show enough of the
  key to recognise it and not enough to use it. `keys create` prints a new
  secret once, because the server never returns it again.
- **The header cannot be replaced.** `api call --header` ignores any
  `Authorization` it is given and always sends the stored key, so a command
  copied from somewhere cannot swap in a credential of its own.

## What a repository can change

A `.statable.yml` in a project may set `site` and `period`, and nothing else.
It is written by whoever can open a pull request, so a file that could set the
API host or a key would be a way to collect keys from everyone who runs the
command in that checkout. Refused keys are reported on stderr.

`STATABLE_API_URL` changes the server, and is read from the environment only.

## Local files

The site cache maps names to site ids so each command costs one request. It is
mode 0600 and authenticated with HMAC-SHA256 under the key: a cache rewritten by
another user is ignored rather than trusted, so it cannot point a site name at
somebody else's site. The cache is written only to a new file, never through a
symlink left at its path, and read only from a regular file of bounded size.

## What it contacts

Only the Statable API (`https://statable.com/api/v1`, or `STATABLE_API_URL`).
No telemetry, no update check, no third-party hosts.

## Verifying a download

Each release is built by GitHub Actions and signed keylessly with cosign. The
signature ties `checksums.txt` to the workflow run and commit that produced it:

```bash
cosign verify-blob checksums.txt \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp 'https://github\.com/key-arg/statable-cli/\.github/workflows/.+' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --check checksums.txt --ignore-missing
```

Each archive also carries an SPDX SBOM. There is deliberately no `curl | sh`
installer.
