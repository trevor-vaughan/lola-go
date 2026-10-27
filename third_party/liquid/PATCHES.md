# lola's copy of osteele/liquid

This directory is `github.com/osteele/liquid` v1.9.2 (MIT, see `LICENSE`), copied from the Go
module cache, with one patch. lola's `go.mod` points the module here with a `replace`
directive. Only `internal/templating` imports it (depguard).

Removed from the release, since nothing compiled needs them: every `_test.go` file, every
`testdata` directory and `cmd/`.

## Patch: flush before a right-trimmed write (`render/trimwriter.go`)

`trimWriter.Write` buffers each write and flushes the buffer before the next one, except when
the trim flag (set by a right trim, `-}}` or `-%}`) is on: then it appended the left-trimmed
bytes to the buffer without flushing. A sequence of right-trimmed objects, `{{ s -}}{{ s -}}…`,
never flushed, so it grew the buffer past lola's output cap, which sees only flushed writes:
2000 × `{{s-}}` with a 64 KiB `s` allocated 383 MiB against a 1 MiB cap, and a 256 KiB template
could buffer about 2.7 GiB (security review C1, Plan 7 spec 85).

The patch flushes the buffer before appending when the trimmed bytes are not empty. The
trimmed bytes then start with a byte that is not a space, so no later left trim can reach into
the flushed buffer, and the output is unchanged (lola's differential corpus renders the same
bytes as unpatched liquid).

```diff
@@ -26,6 +26,14 @@
 	if tw.trim {
 		b = bytes.TrimLeftFunc(b, unicode.IsSpace)
 		tw.trim = false
+		// lola patch (third_party/liquid/PATCHES.md): b starts with a non-space byte, so no
+		// later TrimLeft can reach into the buffer; flush it, as the untrimmed path does, so
+		// chained right-trimmed writes do not accumulate here unbounded.
+		if len(b) > 0 {
+			if _, err = tw.Flush(); err != nil {
+				return 0, err
+			}
+		}
 	} else if _, err = tw.Flush(); err != nil {
 		return 0, err
 	}
```

## Vulnerability checks

Under the `replace`, govulncheck reports this module as `./third_party/liquid` with no version,
so it cannot match advisories for `github.com/osteele/liquid`; it still matches liquid's own
dependencies (`osteele/tuesday`, `gopkg.in/yaml.v2`) by version. Advisories for
`github.com/osteele/liquid` are therefore checked by hand whenever this copy is touched and
before each lola release, as item 3 of the release checklist (`docs/release.md`) says, and each
check is recorded here: its date, the advisory IDs read and each verdict.

- **2026-10-24**, release checklist item 3, on `mvp` `de03ad7`: no advisory exists for
  `github.com/osteele/liquid` in any of the three sources. The GitHub advisory database
  (`/advisories?ecosystem=go&affects=github.com/osteele/liquid`), the Go vulnerability database
  (`vuln.go.dev/index/modules.json` has no entry for the module) and the repository's security
  advisories (`/repos/osteele/liquid/security-advisories`) each returned none; OSV agrees. The
  same queries for `github.com/gin-gonic/gin` returned its advisories, so the queries work.
  Verdict: nothing applies; the tag is not blocked.

## When to drop this copy

When an osteele/liquid release fixes the unbounded buffer (reported upstream by the
maintainer), require that release, remove the `replace` line from `go.mod` and delete this
directory; Plan 7 spec 85, "chained right-trim output stays under the output cap", must still
pass. Until then, an upgrade means copying the new release here and re-applying the patch.
