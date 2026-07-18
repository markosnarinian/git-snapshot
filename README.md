# git-snapshot

[![CI](https://github.com/markosnarinian/git-snapshot/actions/workflows/ci.yml/badge.svg)](https://github.com/markosnarinian/git-snapshot/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/markosnarinian/git-snapshot.svg)](https://pkg.go.dev/github.com/markosnarinian/git-snapshot)

`git-snapshot` captures the current working-tree state in a dedicated Git ref, without adding commits to branch history or changing the real index. It requires Go 1.22+ to build and a working `git` executable at runtime.

## Install

With Go 1.22+ installed:

```sh
go install github.com/markosnarinian/git-snapshot/cmd/git-snapshot@latest
```

This places `git-snapshot` in `$(go env GOPATH)/bin` (or `$GOBIN`); make sure that directory is on `PATH`. `git snapshot version` reports the installed module version.

The executable works standalone (`git-snapshot create`). Because Git maps `git snapshot …` to an executable named `git-snapshot` on `PATH`, the same installation enables the external-command form (`git snapshot create`). For a machine without Go, download a static pre-built binary for macOS, Linux, or Windows (amd64/arm64) from [GitHub Releases](https://github.com/markosnarinian/git-snapshot/releases) — checksums are in `SHA256SUMS` — and copy it to a `PATH` directory. Optional completion files are under `completions/`; the man page is `man/git-snapshot.1`. Copy them manually to your shell's completion directory and manpath if wanted.

## Design and architecture

`cmd/git-snapshot` is the process/signal/exit-code adapter. `internal/app` contains CLI/config parsing, repository discovery and Git execution, safety preflight, snapshot metadata and stream verification, CAS ref updates, inspection, and restore/mutation operations. Git remains the storage engine: each snapshot is an ordinary commit and tree, while a linear first-parent stream lives at a full ref (default `refs/snapshots/default`). Commits carry `Git-Snapshot-*` ownership, ref, base, version, and timestamp trailers. The first snapshot is parented to the chosen base commit (or has no parent for an unborn repository); later snapshots parent the previous snapshot.

### Create side effects and guarantees

On a successful non-dry-run `create`, exactly these persistent Git-side effects are expected:

1. blobs and tree objects needed for the captured worktree are written to the repository object database;
2. one snapshot commit object is written (and, if enabled, signed);
3. only the exact configured snapshot ref is atomically created/advanced with compare-and-swap; and
4. by default, a reflog is created/updated for that ref.

The working tree, `HEAD`, branch refs, and real index are not changed. Capture copies/materializes the real index into a private mode-0700 temporary directory and stages there. Existing streams are fully ownership/shape-verified before extension. Protected namespaces, symbolic refs, refs outside the configured namespace, active locks, in-progress operations/conflicts, dirty submodules, and untracked embedded repositories are refused unless the relevant explicit exception exists. Ref CAS prevents silently clobbering concurrent updates.

Objects are immutable but may remain unreachable after failure or later `drop`/`delete`, until Git garbage collection. **`create --dry-run` still runs `git add` and `write-tree` against a temporary index, so it can write blob/tree objects to the object database; it does not create a commit or update a ref/reflog.**

By default non-ignored untracked files are included. **Ignored files are excluded. `--include-ignored` can permanently put credentials, secrets, dependencies, and build output into Git objects; it prints a warning and requires typing `yes` unless `--yes` is supplied.**

## Commands and examples

Flags may appear before or after positional selectors.

```sh
git snapshot create --message 'before refactor'
git snapshot create --message-file note.txt --no-include-untracked --dry-run
git snapshot list --limit 10 --show-ref --show-base --show-size
git snapshot list --reverse --format '%d %h %cI %s %r %b'
git snapshot show latest --stat
git snapshot show 2 --name-only
git snapshot diff latest worktree --patch
git snapshot diff 3 0 --stat                  # from older to latest
git snapshot verify                           # whole stream
git snapshot verify 0                         # one snapshot
git snapshot restore latest --destination ../snapshot-copy
git snapshot restore 1 --worktree --dry-run
git snapshot restore 1 --worktree --force --yes --staged
git snapshot drop --count 2 --yes
git snapshot delete --yes
git snapshot config get snapshot.ref
git snapshot config set --global snapshot.retention 20
git snapshot config unset --global snapshot.retention
git snapshot config list --effective --show-origin
git snapshot version
```

`show` allows at most one of `--stat`, `--name-only`, `--patch`; `diff` does likewise and defaults to patch. With no endpoints, diff compares latest to `worktree` (or `HEAD` in a bare repository); `HEAD` and `worktree` are destination-only. `list --format` supports `%d`, `%H`, `%h`, `%cI`, `%s`, `%r`, `%b`.

### Selectors

A selector is `latest` (same as `0`), a non-negative first-parent distance (`0` newest), or a **full** SHA-1/SHA-256 object ID. IDs must belong to the selected stream. `show`, `diff`, `verify`, and `restore` accept `--allow-unreachable` for a full CLI-owned ID outside the current stream; metadata must still claim the selected ref. Short IDs and arbitrary revisions are intentionally rejected.

### Restore behavior

Safe restore requires `--destination`; the destination must be new/empty unless `--overwrite`, must not be Git metadata, and extraction is staged privately before publication. Overwriting copies snapshot entries but does not promise to remove unrelated existing destination entries. `--dry-run` only lists paths.

`--worktree` is explicit and previews affected paths. Any dirty state, operation/conflict, or ignored-file collision requires `--force`; destructive execution then requires confirmation unless `--yes`. It replaces snapshot paths and removes tracked/non-ignored-untracked paths absent from the snapshot, but refuses deletion of nested repository metadata. The real index remains untouched unless `--staged` (which requires `--worktree`). Mid-copy filesystem failures can leave a partially changed destination/worktree and are reported as such.

### Retention

`snapshot.retention` / `--retention` is a maximum stream length; `0` means unlimited. When an existing stream already has that many snapshots, create is refused. Retention never automatically drops commits or rewrites history. Use confirmed `drop` to move back while leaving at least one snapshot, or `delete` to remove the complete exact stream. Drop writes a reflog entry; both operations may make objects unreachable, and reflogs/GC determine eventual reclamation.

## Flags

Every non-`config` command accepts: `--repo`, `--ref`, `--namespace`, `--base`, `--include-untracked`/`--no-include-untracked`, `--include-ignored`/`--no-include-ignored`, `--create-reflog`/`--no-create-reflog`, `--message-template`, `--sign`/`--no-sign`, `--signing-key`, `--output-format human|json`, `--color auto|always|never`, `--yes`, `--restore-destination`, `--retention`, `--lock-timeout`, `--config-file`, `--json`, `--quiet`, and `--verbose`.

Command-specific flags: `create`: `--message`, `--message-file`, `--allow-in-progress`, `--allow-dirty-submodules`, `--dry-run`; `list`: `--limit`, `--reverse`, `--format`, `--show-ref`, `--show-base`, `--show-size`; `show`: `--stat`, `--name-only`, `--patch`, `--format`, `--allow-unreachable`; `diff`: `--stat`, `--name-only`, `--patch`, `--allow-unreachable`; `verify`: `--allow-unreachable`; `restore`: `--destination`, `--worktree`, `--overwrite`, `--force`, `--staged`, `--dry-run`, `--allow-unreachable`; `drop`: `--count`. Config actions accept `--repo`, mutually exclusive `--global`/`--local`/`--file`, plus `--effective`, `--show-origin`, `--json` (applicability depends on action; unscoped writes default local).

## Configuration and environment

Precedence, lowest to highest, is: built-in defaults → **explicit config file** (`--config-file` or `GIT_SNAPSHOT_CONFIG_FILE`) → global Git config → repository-local Git config → environment → command-line flags. Thus the explicit file is deliberately the lowest config layer, not an override. `--repo` bootstraps local-config discovery and, like every CLI flag, overrides environment and configured values. `config get` without a scope and `config list` show effective values; scoped `get/set/unset` use `--global`, `--local`, or `--file` (default mutation scope is local).

| Git key                       | Environment                        | Default / valid values                              |
| ----------------------------- | ---------------------------------- | --------------------------------------------------- |
| `snapshot.repo`               | `GIT_SNAPSHOT_REPO`                | `.`                                                 |
| `snapshot.ref`                | `GIT_SNAPSHOT_REF`                 | `refs/snapshots/default`                            |
| `snapshot.namespace`          | `GIT_SNAPSHOT_NAMESPACE`           | `refs/snapshots/`                                   |
| `snapshot.base`               | `GIT_SNAPSHOT_BASE`                | `HEAD`                                              |
| `snapshot.includeUntracked`   | `GIT_SNAPSHOT_INCLUDE_UNTRACKED`   | `true`                                              |
| `snapshot.includeIgnored`     | `GIT_SNAPSHOT_INCLUDE_IGNORED`     | `false`                                             |
| `snapshot.createReflog`       | `GIT_SNAPSHOT_CREATE_REFLOG`       | `true`                                              |
| `snapshot.messageTemplate`    | `GIT_SNAPSHOT_MESSAGE_TEMPLATE`    | `git-snapshot: {createdAt}`; also `{ref}`, `{base}` |
| `snapshot.sign`               | `GIT_SNAPSHOT_SIGN`                | `false`                                             |
| `snapshot.signingKey`         | `GIT_SNAPSHOT_SIGNING_KEY`         | empty (setting it implies signing)                  |
| `snapshot.outputFormat`       | `GIT_SNAPSHOT_OUTPUT_FORMAT`       | `human`; `human` or `json`                          |
| `snapshot.color`              | `GIT_SNAPSHOT_COLOR`               | `auto`; `auto`, `always`, `never`                   |
| `snapshot.yes`                | `GIT_SNAPSHOT_YES`                 | `false`                                             |
| `snapshot.restoreDestination` | `GIT_SNAPSHOT_RESTORE_DESTINATION` | empty                                               |
| `snapshot.retention`          | `GIT_SNAPSHOT_RETENTION`           | `0` (non-negative)                                  |
| `snapshot.lockTimeout`        | `GIT_SNAPSHOT_LOCK_TIMEOUT`        | `5s` (non-negative Go duration)                     |
| `snapshot.defaultCommand`     | `GIT_SNAPSHOT_DEFAULT_COMMAND`     | `create`; `create` or `help`                        |

Boolean config/environment values use Go boolean syntax accepted by `strconv.ParseBool`; config validation messages recommend `true`/`false`. `GIT_SNAPSHOT_CONFIG_FILE` selects the explicit file and is not a Git key.

## Exit codes

| Code | Meaning                                                     |
| ---: | ----------------------------------------------------------- |
|    0 | success                                                     |
|    1 | operational/unclassified failure                            |
|    2 | usage or invalid configuration                              |
|    3 | safety refusal or cancelled confirmation                    |
|    4 | repository/ref/object/selector/config value not found       |
|    5 | lock or concurrent ref update                               |
|    6 | ownership, metadata, object, or stream verification failure |

Errors state whether anything may have changed. `--json`/JSON output mode emits structured errors where detectable from arguments/environment.

## Threat model and limitations

- **Data loss:** default capture is non-destructive; destination restore is preferred. Worktree restore is intentionally destructive only behind `--worktree`, preview, cleanliness checks, `--force`, and confirmation. Filesystem failure is not transactional and backups remain the user's responsibility.
- **Ref clobber:** protected namespaces, namespace containment, direct-ref checks, ownership verification, expected-old-value CAS, and lock retries defend against accidental/adversarial ref movement. A process with repository write access can still modify refs/objects outside this program.
- **Concurrency:** known lock files are refused and ref changes are CAS-protected. The working tree can still change while capture/restore runs; no global filesystem transaction or lock can prevent another process editing files.
- **Untrusted config:** Git config and environment control repository/ref, signing, restore defaults, inclusion, and confirmations. Do not run with attacker-controlled config/environment; inspect `config list --show-origin`. Git hooks are not used directly, but Git configuration and signing programs remain part of Git's trust boundary.
- **Nested repositories/submodules:** untracked embedded repositories are refused during capture; dirty submodules are refused unless only their gitlink commits are explicitly accepted. Worktree restore refuses deleting nested repository metadata. Snapshot nested repositories separately; a gitlink does not contain a submodule's dirty files.
- **Secrets:** ignored files are not a security boundary once `--include-ignored` is selected. Written objects may persist after refs are removed.

## Development and releases

```sh
go build ./...
go vet ./...
go test ./...
go test -race ./...
```

CI (`.github/workflows/ci.yml`) runs vet, build, and race tests on Linux, macOS, and Windows for every push and pull request. Releases are Git tags: pushing a semver tag (e.g. `v1.2.3`) triggers `.github/workflows/release.yml`, which tests, cross-compiles static (`CGO_ENABLED=0`, `-trimpath`) macOS/Linux/Windows binaries for amd64/arm64, publishes them with `SHA256SUMS` as a GitHub release, and pings the Go module proxy so the version appears on [pkg.go.dev](https://pkg.go.dev/github.com/markosnarinian/git-snapshot). The Go toolchain stamps the tag as the module version, so both `go install …@v1.2.3` builds and released binaries report it in `git snapshot version`.

## License

[MIT](LICENSE)
