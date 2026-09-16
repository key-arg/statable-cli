# Changelog

What changed for someone using `statable`, release by release. The commit
history has the reasoning; this file has the consequences.

Versions follow [Semantic Versioning](https://semver.org/). Before 1.0 a minor
release may add commands and flags but does not remove or rename them.

## Unreleased

### Added

- A Scoop bucket for Windows: `scoop bucket add statable
  https://github.com/key-arg/scoop-bucket` then `scoop install statable`.

## [0.4.3] — 2026-09-16

### Fixed

- A failed `check` no longer labels its own failure `ok`. The line a person
  reads is now `result: below the minimum of 100`; scripts still read `ok`
  in JSON and CSV, and the exit code is unchanged.

## [0.4.2] — 2026-09-16

### Fixed

- The image builds. One build context serves every platform, so the binary
  lives under `$TARGETPLATFORM/` rather than at the root of the context.

## [0.4.1] — 2026-09-16

### Fixed

- The container image 0.4.0 describes is actually published. The release that
  introduced it ran with an empty Docker Hub token, so the image build was
  skipped exactly as designed and the archives went out alone.

## [0.4.0] — 2026-09-16

### Added

- A container image, `statable/statable`, for `linux/amd64` and `linux/arm64`.
  It is the binary this release already built and signed, on distroless: no
  shell, no package manager, running as a non-root user.

  ```bash
  docker run --rm -e STATABLE_API_KEY statable/statable query \
    --metric visitors,pageviews --range 7d
  ```

  A container has no keyring and no home directory to write to, so the key
  comes from `STATABLE_API_KEY` and the CLI does not prompt.

### Development

- `gen-docs` takes `-web`, `-manifest` and `-version`, and writes the command
  reference published at [statable.com/docs/cli/commands](https://statable.com/docs/cli/commands/)
  from the command tree. Release archives are unaffected.

## [0.3.0] — 2026-09-15

The whole Stats API, reading and writing.

### Added

- `sites create`, `sites edit`, `sites delete`
- `goals create`, `goals edit`, `goals delete`
- `funnels create`, `funnels edit`, `funnels delete`, with a step shorthand:
  `page:/pricing`, `event:Signup`, `goal:9`, `scroll:90`
- `keys`, `keys create`, `keys rotate`, `keys revoke`, `keys events`
- `settings`, and `settings set tracking|countries|blocked-ips|hostnames|public-dashboard`
- `auth register`: create an account and a first key from an emailed code,
  without a browser
- `STATABLE_NO_KEYRING`: skip the system keyring for one invocation, for
  containers and CI runners where waiting for it only times out

### Changed

- Destructive commands (`sites delete`, `goals delete`, `funnels delete`,
  `keys rotate`, `keys revoke`) ask first, name what they are about to change,
  and refuse without `--yes` where nobody can answer.

Every command and flag from 0.2.2 behaves as before.

## [0.2.2] — 2026-09-13

### Fixed

- Releases publish the Homebrew cask again. 0.2.1 failed at that step.

## [0.2.1] — 2026-09-13

### Fixed

- The mise instruction named the wrong binary. It is
  `mise use -g "ubi:key-arg/statable-cli[exe=statable]"`.

## [0.2.0] — 2026-09-10

### Added

- `goals`: the conversion goals defined on a site
- `snippet`: the install tag for a site, ready to paste

### Security

- Built with Go 1.26.8. 0.1.0 was built with 1.26.3, which carries seven
  standard-library vulnerabilities on the path of every HTTPS request this
  client makes. Upgrade from 0.1.0.

## [0.1.0] — 2026-09-10

First release.

- Reading: `now`, `stats`, `series`, `top`, `query`, `props`, `funnels`,
  `funnel`, `subscription`, `sites`, `sites use`
- `check`: assert a metric is within bounds; exit 3 when it is not
- `api call`, `api search`, `api describe`: any endpoint, and the API's own
  description of itself
- `auth login`, `auth status`, `auth logout`, with the key in the system keyring
- `--format human|json|csv` on every command, and exit codes a script can
  branch on: 0, 1 action required, 2 failed, 3 threshold, 64 bad command line
- Archives for macOS, Linux and Windows on amd64 and arm64, with shell
  completions, man pages, an SBOM and a keyless cosign signature

[0.3.0]: https://github.com/key-arg/statable-cli/releases/tag/v0.3.0
[0.2.2]: https://github.com/key-arg/statable-cli/releases/tag/v0.2.2
[0.2.1]: https://github.com/key-arg/statable-cli/releases/tag/v0.2.1
[0.2.0]: https://github.com/key-arg/statable-cli/releases/tag/v0.2.0
[0.1.0]: https://github.com/key-arg/statable-cli/releases/tag/v0.1.0
