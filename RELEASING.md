# Releasing message-sync

Releases are explicit, tag-driven events. Ordinary development merges do not publish images.

## Release identity

Every new release must keep these values aligned:

```text
Git tag        vX.Y.Z
VERSION        X.Y.Z
binary         X.Y.Z
container tag  X.Y.Z
GitHub Release vX.Y.Z
```

Supported release tags use `MAJOR.MINOR.PATCH` with an optional pre-release suffix, for example `v0.3.0` or `v0.3.0-rc.1`. Build metadata is not used for container releases.

`VERSION` is the repository's expected release version. The Git tag is the authoritative release event: changing or merging `VERSION` by itself never publishes.

## Historical version note

The legacy `VERSION`-change workflow successfully published `0.2.0` on 2026-09-02 from commit `2c759b8bc517379ac3c4e41c0eb9aa7c9563a817` (release workflow run #2). There are no corresponding Git tags or GitHub Releases because the old process did not create them.

The old workflow subsequently published lower-numbered `0.1.x` versions through `0.1.8`, so historical image numbering is non-monotonic. This is genuine published history and must not be rewritten or reused. In particular:

- do not reuse `0.2.0`;
- do not move or rebuild an existing exact-version image;
- the next new release must compare greater than `0.2.0` (for example, `0.3.0` for the next feature release).

The current `VERSION` remains `0.1.8` until a dedicated release PR chooses the next release version.

## Normal development

Feature and fix PRs:

- do not bump `VERSION`;
- record user-visible changes under `[Unreleased]` in `CHANGELOG.md` when appropriate;
- keep `main` releasable;
- do not create release tags.

Normal pushes and merges to `main` do not run the publishing workflow.

## Prepare a release

1. Choose the next version according to SemVer. Before 1.0:
   - patch: backward-compatible bug fixes;
   - minor: meaningful backward-compatible feature/config/API evolution;
   - `1.0.0`: when compatibility expectations are intentionally declared stable.
2. Create a small release branch/PR, normally changing only `VERSION` and `CHANGELOG.md`.
3. Move the relevant `[Unreleased]` entries into a dated section for the chosen version and reset `[Unreleased]` for future work.
4. Set `VERSION` to exactly the version without a leading `v`.
5. Use a release PR title such as `release: 0.3.0`.
6. Run the normal test/vet gate and merge the release PR after review.

Merging the release PR does not publish images.

## Execute a stable release

After the release PR is merged, update local `main` and identify the exact intended commit:

```sh
git switch main
git pull --ff-only origin main
cat VERSION
git log -1 --oneline
```

Create and push an annotated tag whose version exactly matches `VERSION`:

```sh
version="$(tr -d '[:space:]' < VERSION)"
git tag -a "v$version" -m "message-sync v$version"
git push origin "v$version"
```

The workflow then:

1. checks out the tagged commit;
2. validates the tag format and requires tag version == `VERSION`;
3. runs `go test ./...`;
4. runs `go vet ./...`;
5. builds a release-mode binary and verifies `message-sync version`;
6. requires Docker Hub credentials;
7. refuses to overwrite either registry's exact-version image;
8. publishes `linux/amd64` and `linux/arm64` images to GHCR and Docker Hub;
9. publishes both the exact version and `latest` for a stable release;
10. verifies the published GHCR image reports the exact version;
11. creates the matching GitHub Release with generated release notes.

For a stable `0.3.0`, the image tags are:

```text
ghcr.io/vm75/message-sync:0.3.0
ghcr.io/vm75/message-sync:latest

docker.io/<DOCKERHUB_USERNAME>/message-sync:0.3.0
docker.io/<DOCKERHUB_USERNAME>/message-sync:latest
```

## Execute a pre-release

Prepare `VERSION` with the full pre-release version, for example:

```text
0.3.0-rc.1
```

Then create and push the matching annotated tag:

```sh
git tag -a v0.3.0-rc.1 -m "message-sync v0.3.0-rc.1"
git push origin v0.3.0-rc.1
```

A pre-release publishes only:

```text
ghcr.io/vm75/message-sync:0.3.0-rc.1
docker.io/<DOCKERHUB_USERNAME>/message-sync:0.3.0-rc.1
```

It does not move `latest`, and the GitHub Release is marked as a pre-release.

## Failure behavior

A mismatch such as:

```text
tag     v0.3.1
VERSION 0.3.0
```

fails in the verify job before registry login, image build/push, or GitHub Release creation.

If tests, vet, credentials, publishing, image verification, or GitHub Release creation fail, inspect the workflow before taking further action. Do not move/reuse an already published exact release version to repair a bad release.

## Exact versions are immutable

Once an exact version is published, its Git tag and exact image tags are immutable release identities. Never delete/re-point the tag and never rebuild or overwrite `:X.Y.Z` from another commit.

If a release is bad, fix the code and publish a new version (normally a new patch), for example `0.3.1`.

Only moving aliases such as `latest` may change, and `latest` moves only on stable releases.

## Registry credentials

GHCR uses the workflow `GITHUB_TOKEN`:

- username: GitHub Actions actor;
- registry: `ghcr.io`;
- publish permission: `packages: write`.

Docker Hub uses repository secrets:

```text
DOCKERHUB_USERNAME
DOCKERHUB_TOKEN
```

No registry credentials belong in the repository.

## Verify a published release

After the workflow succeeds, verify the release page and both registries. For example:

```sh
version=0.3.0
docker pull "ghcr.io/vm75/message-sync:$version"
test "$(docker run --rm "ghcr.io/vm75/message-sync:$version" version)" = "$version"

docker pull "docker.io/vm75/message-sync:$version"
test "$(docker run --rm "docker.io/vm75/message-sync:$version" version)" = "$version"
```

For stable releases, also verify `latest` resolves to the newly released stable image. For pre-releases, verify `latest` did not move.
