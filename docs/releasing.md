# Releasing HIVE

Two repositories are involved, and the difference matters.

| Repository | Role |
|---|---|
| the development repository (private) | Every commit lands here first, behind `make gate` and the CI, Lean Build and Publish container image workflows. |
| `Chubby-Honey-Bee/hive` | Release. The module path, the image name, the badges and `.goreleaser.yaml`'s `release.github` all point here. Tags are pushed here, and only here. |

A release is a push of `main` from the development repository to the release repository, followed by a `vX.Y.Z` tag there. Nothing else cuts one.

## Before tagging

1. `main` is green: `make gate` locally, and the three workflows on the development repository for the commit to be tagged.
2. `CHANGELOG.md` has a `## [X.Y.Z] — YYYY-MM-DD` section under `## [Unreleased]`, holding what the release contains and nothing that is not on `main`; `[Unreleased]` is empty. The reference links at the end of the file name the tag.
3. `.goreleaser.yaml` is valid. `goreleaser check` says so when goreleaser is installed; without it, validate the file against the published schema (`https://goreleaser.com/static/schema.json`). The Release workflow runs the same configuration on the tag and fails there otherwise.
4. The release repository has Actions enabled. `release.yml` asks for `contents: write` and `publish-image.yml` for `packages: write`; both are declared in the workflows themselves.

## Cutting it

```bash
git remote add release https://github.com/Chubby-Honey-Bee/hive.git   # once
git push release main
# wait for main's runs to appear under Actions
git tag -a vX.Y.Z -m "HIVE X.Y.Z"
git push release vX.Y.Z
```

`release.yml` runs `goreleaser` and opens a **draft** release with six archives, one per operating system and architecture (Linux, macOS and Windows on amd64 and arm64), each holding `chb`, `chb-mcp`, `LICENSE` and `README.md`, and `checksums.txt`. `publish-image.yml` pushes `ghcr.io/chubby-honey-bee/hive:X.Y.Z`, `:X.Y` and `:latest`. Read the draft, then publish it.

Push the tag only after `main`'s runs have appeared under Actions: a tag that arrives seconds after the first push of `main` into an empty repository starts no workflow. A run for the tag appears within a minute; when none does, delete the tag on the release remote and push it again (`git push release :refs/tags/vX.Y.Z && git push release vX.Y.Z`), which starts both tag workflows.

If the Release run fails, fix the cause on `main`, push it to both repositories, delete the tag in the release repository (`git push release :refs/tags/vX.Y.Z`, then `git tag -d vX.Y.Z`) and tag again. This is safe only while the release is still a draft; a published tag is never moved.

## Making it public

Two separate settings, in this order:

1. The repository `Chubby-Honey-Bee/hive`: Settings → General → Visibility. After this, `go install github.com/Chubby-Honey-Bee/hive/cmd/chb@vX.Y.Z` resolves.
2. The package `ghcr.io/chubby-honey-bee/hive`: the organisation's Packages page → the package → Package settings → Change visibility. After this, the README's MCP configuration and the VS Code badge, which pull anonymously, work.

Until both are done, building from source, as the README's Install section shows, is the path that works for others.

## Tag form

The tag is `vX.Y.Z`. goreleaser reports the version as `X.Y.Z` and stamps it into the binaries (`chb --version` → `chb version X.Y.Z`); `publish-image.yml` strips the `v` the same way, so the image tag, the image's binaries and the archives' binaries agree.
