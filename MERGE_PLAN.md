# Upstream merge plan

## Scope

Merge the 10 commits on `readium/go-toolkit:develop` from `d7d3fed`
(`v0.14.0`) through `881320b` into `Nohles/go-toolkit:develop`.

The fork currently has:

- `origin/develop` at `a95eab9`, which already merged upstream through
  `d7d3fed`.
- One local commit, `e082414`, adding numeric comic chapter sorting.
- A clean worktree before this plan was added.

The incoming upstream commits add:

1. GitHub Actions dependency updates.
2. EPUB parsing and DRM-detection optimizations.
3. Bare and packaged WebPub parsing improvements.
4. Go dependency updates.
5. Parallel M4B chapter parsing.
6. Local and remote streaming improvements.
7. HTML-to-guided-navigation conversion.
8. The `v0.15.0` changelog.
9. Retained M4B parsing/serving caches.
10. The `v0.15.1` changelog.

The total upstream diff is 89 files, approximately 8,078 additions and 683
deletions.

## Dry-run result

A disposable-clone merge against upstream commit `881320b` produced 12 textual
conflicts:

| Conflict | Cause | Proposed resolution |
| --- | --- | --- |
| `CHANGELOG.md` | The fork has an `Unreleased` section where upstream inserts `0.15.0` and `0.15.1`. | Keep the fork's `Unreleased` section first, then include both upstream release sections unchanged. |
| `go.mod` | The fork changed the module to `github.com/nohles/go-toolkit` and Go to `1.26.0`; upstream remains on the Readium module and Go `1.25.8` while updating dependencies. | Keep the fork module and Go `1.26.0`, while accepting upstream's dependency versions. |
| `pkg/asset/asset_publication.go` | Import-path rename only. | Take upstream logic and use `github.com/nohles/go-toolkit/...` imports. |
| `pkg/parser/audio/mp4.go` | Import-path rename only. | Take upstream M4B concurrency/cache logic and use fork imports. |
| `pkg/parser/audio/toc.go` | Import-path rename only. | Take upstream guided-navigation fragment formatting and use fork imports. |
| `pkg/parser/epub/media_overlay_service.go` | Import-path rename only. | Take upstream guided-navigation logic and use fork imports. |
| `pkg/parser/epub/parser.go` | Import-path rename only. | Take upstream EPUB optimization and use fork imports. |
| `pkg/parser/epub/parser_smil.go` | Import-path rename only. | Take upstream guided-navigation types and use fork imports. |
| `pkg/parser/epub/parser_smil_test.go` | Import-path rename only. | Take upstream test changes and use fork imports. |
| `pkg/parser/webpub/parser.go` | Import-path rename only. | Take upstream WebPub implementation and use fork imports. |
| `pkg/protection/epub.go` | Import-path rename only. | Take upstream DRM optimization and use fork imports. |
| `pkg/pub/service_guided_navigation.go` | Import-path rename only. | Take upstream guided-navigation model and use fork imports. |

Ten of the twelve conflicts are therefore mechanical consequences of the
fork-wide module rename, not competing implementations.

## Overlap and behavior decisions

### Audiobooks: combine both implementations

The important audiobook edits overlap in files, but Git merged the substantive
logic automatically in the dry run.

Retain the fork behavior:

- Infer missing/binary audio media types from file extensions, including M4A,
  M4B, MP4, and FLAC.
- Naturally sort numbered tracks (`2` before `10`).
- Emit publication-relative HREFs.
- Allow common sidecars while still requiring at least one audio file.
- Preserve the fork's audiobook metadata enrichment and tests.

Accept the upstream behavior:

- Fetch scattered M4B chapter-title samples concurrently.
- Apply `WithConcurrency` within a file as well as across files.
- Add `WithRetainedCache` and serve already-probed byte ranges from memory.
- Share remote resource length metadata to reduce repeated HEAD requests.
- Upgrade `github.com/abema/go-mp4` and related dependencies.
- Use the new guided-navigation media-fragment formatter for chapter links.

These features are complementary. There is no current reason to discard either
implementation.

### Comics: retain the fork implementation

Upstream does not replace the fork's folder-level CBZ/CBR manifests, table of
contents, relative chapter HREFs, or numeric chapter sorting. Keep those changes
as-is.

The risk is indirect: upstream changes media sniffing order so that OPDS, LCP,
and WebPub content are tested before generic archives. Re-run the fork's comic
parser and media-type tests to ensure folders of comic archives still select the
image parser and maintain chapter order.

### WebPub and media sniffing: accept upstream, verify parser selection

Accept upstream's bare-manifest and relative-resource support. This is a large,
useful implementation rather than a fork-equivalent feature.

Explicitly test ambiguous inputs because both sides changed detection:

- Bare `.audiobook` manifests select the WebPub parser.
- Unstructured folders/archives of M4A, M4B, MP3, or FLAC select the audio
  parser.
- Folder-level CBZ/CBR collections select the image parser.
- A ZIP containing `manifest.json` selects WebPub rather than CBZ/ZAB.
- Nil media types do not panic any parser candidate.

### Guided navigation and EPUB: accept upstream, note API changes

Accept the new `pkg/guidednavigation` model and HTML converter. Upstream deletes
`pkg/manifest/guided_navigation.go`, changes SMIL/media-overlay types, replaces
the EPUB content service with guided navigation, and changes
`IdentifyEPUBProtection` to return the parsed encryption document.

The repository's internal callers are migrated by upstream, but downstream
users of the fork may need source changes. Treat this as a release-note item and
search any consuming repositories before publishing a new fork release.

## Implementation sequence

1. Commit or otherwise preserve this plan, then add a persistent `upstream`
   remote pointing to `https://github.com/readium/go-toolkit.git`.
2. Create a dedicated branch from `develop`, including local commit `e082414`.
3. Fetch `upstream/develop` and confirm its expected tip (the audited tip was
   `881320b`; re-audit if it has moved).
4. Fetch upstream Git LFS objects before checkout/merge:
   `git lfs fetch upstream upstream/develop`. The explicit remote-tracking ref
   avoids resolving the fork's local `develop` branch. Upstream adds a roughly
   25 MB audiobook fixture; a normal merge can otherwise try the fork remote
   first and fail its smudge step.
5. Merge with `--no-commit --no-ff` so the result can be reviewed before
   committing.
6. Resolve the 10 code conflicts by taking upstream behavior and translating
   every new internal import from `github.com/readium/go-toolkit` to
   `github.com/nohles/go-toolkit`.
7. Resolve `go.mod` by keeping the fork module and Go `1.26.0`, accepting
   upstream dependency updates, then run `go mod tidy` and review `go.sum`.
8. Resolve `CHANGELOG.md` by retaining the fork's `Unreleased` section above
   upstream `0.15.1` and `0.15.0`.
9. Search the entire tree for accidental Readium module imports and unresolved
   conflict markers.
10. Format changed Go files and run the validation below.
11. Review the final diff specifically for fork-only audio, comic, manifest-list,
    and module-name changes before creating the merge commit.

## Validation

Run, in order:

1. Focused tests:
   - `go test ./pkg/parser/audio`
   - `go test ./pkg/parser/image`
   - `go test ./pkg/parser/webpub`
   - `go test ./pkg/mediatype`
   - `go test ./pkg/parser/epub ./pkg/protection`
   - `go test ./pkg/guidednavigation/... ./pkg/pub`
   - `go test ./pkg/streamer`
2. Race-sensitive cache/streaming tests:
   - `go test -race ./pkg/parser/audio ./pkg/archive ./pkg/fetcher`
3. Full suite:
   - `go test ./...`
4. Static checks used by the repository's CI.
5. Confirm the LFS audiobook fixture is available from the fork after the merge,
   not only from upstream.

The dry-run candidate reached dependency compilation after resolving conflicts.
The full test command could not finish on the current machine because the disk
had less than 600 MB free and Go exhausted temporary build space. This was an
environment failure (`no space left on device`), not a reported source
compilation or test assertion failure. Free several gigabytes before performing
the real validation.

## Recommended decisions

Proceed with the merge using these defaults:

- Keep the fork module path.
- Keep Go `1.26.0`.
- Keep all fork audiobook and comic behavior.
- Accept upstream's M4B performance work, WebPub parser, streaming improvements,
  EPUB optimizations, guided-navigation implementation, and dependency updates.
- Preserve both sides' changelog entries.

No unresolved implementation-choice conflict currently requires an ours/theirs
decision. The two items worth confirming before implementation are policy
rather than merge mechanics: whether the fork intentionally requires Go
`1.26.0`, and whether downstream consumers are ready for upstream's
guided-navigation API changes.
