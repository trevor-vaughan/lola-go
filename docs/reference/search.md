# `lola search`

```
lola search <query>
```

Alias: `find`. Search your cache, then every [marketplace](../guides/marketplaces.md#marketplaces)
catalog you have fetched, case-insensitively by name, description and keyword (tags), listing each
hit by what `lola install` takes (`@acme/formatter` for a catalog entry). It never fetches. `lola
cache search` searches the cache alone. When `markets.yaml` cannot be read, it lists your cache's
hits with a warning saying why, as the install pickers offer your cache alone then.

Refusals: no query: `lola search needs a query`.

JSON: `results` (each `name`, `description`, `version`, `keywords`, `where` (`cache` or
`@<market>`) and `install`) and `warnings`. `--plain` rows are name, version, where,
description and install.
