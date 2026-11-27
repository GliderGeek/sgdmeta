# sgdmeta — harvester

Haalt de metadata van Staten-Generaal Digitaal op bij de SRU-API van
repository.overheid.nl en bouwt daaruit `sgdmeta.sqlite3`. Zie de
[README in de root](../README.md) voor wat sgdmeta is en hoe je de data gebruikt;
dit is alleen nodig om de data te vernieuwen of na te gaan hoe hij tot stand kwam.

## De data opnieuw harvesten

Vereist [uv](https://docs.astral.sh/uv/). Alle commando's vanuit deze map:

```sh
uv run sgdmeta plan       # fase 1: werklijst afleiden uit de API  (19 requests)
uv run sgdmeta harvest    # fase 2: rauwe XML ophalen             (458 requests, ~40 min)
uv run sgdmeta index      # fase 3: lokaal parsen naar SQLite      (geen requests)
uv run sgdmeta verify     # controles + sprekersdekking
uv run sgdmeta status     # wat staat er al?
```

`harvest` is hervatbaar: onderbreken mag, opnieuw draaien pakt de draad op.
Standaard gaat er één request per 5 seconden uit, want de server rate-limit
met HTTP 429. Rustiger kan met `--interval 10`.

Resultaat in `data/`:

- `raw/` — de onbewerkte API-responses, gzip. Bewaren: hiermee kun je fase 3
  ongelimiteerd opnieuw draaien zonder de server aan te raken.
- `sgdmeta.sqlite3` — de doorzoekbare database.

## Schema

Het schema staat in `src/sgdmeta/index.py` (`SCHEMA`). De app in `../app` leest
de database en herkent hem aan `PRAGMA user_version` (`SCHEMA_VERSION`). Verander
je iets dat lezers merken, hoog dan `SCHEMA_VERSION` op en pas `minSchema`/`maxSchema`
in `../app/db.go` aan.

## Ontwikkelen

```sh
uv run pytest        # tests draaien tegen echte API-responses in tests/fixtures/
uv run ruff check src tests
```

`HandleidingSRU2.0.pdf` is de handleiding van de SRU-API van repository.overheid.nl,
de bron waar `sgdmeta` de data vandaan haalt.
