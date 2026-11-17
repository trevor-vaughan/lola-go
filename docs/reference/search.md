# `lola search`

Command reference, for lola users; back to [Using lola](../usage.md) and the [README](../../README.md).

```
lola search <query>
```

Alias: `find`. Search your [cache](cache.md) (the modules lola has fetched), then every
[marketplace](../guides/marketplaces.md#marketplaces) catalog you have fetched, case-insensitively
by name, description and keyword (tags). Each hit is listed by what `lola install` takes: a cached
module's name, or `@<market>/<module>` for a catalog entry (`@acme/formatter`). It never fetches.
`lola cache search` searches the cache alone. When your marketplaces file
([`markets.yaml`](../usage.md#files-lola-keeps)) cannot be read, it lists your cache's hits with a
warning saying why, as the pickers of `lola install` offer your cache alone then.

Refusals: no query: `lola search needs a query`.

Output, in the three modes of the [output contract](output-contract.md):

- Human: one row per hit, its `install` value, version, `where` and description; `no matches` when
  nothing matches. Warnings print on stderr.
- `--json`: `results` (each `name`, `description`, `version`, `keywords`, `where` (`cache` or
  `@<market>`) and `install`) and `warnings`.
- `--plain`: rows of name, version, where, description and install.
