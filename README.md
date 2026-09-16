# sgdmeta

Op officielebekendmakingen.nl staat de collectie Staten-Generaal Digitaal: de
parlementaire documenten van 1814 tot 1995, van belang voor parlementair historisch onderzoek.
De zoekfunctie is meer afgestemd om specifieke documenten te vinden
dan om een globaal beeld te krijgen van een periode.
Voor dergelijke vragen is de metadata van deze bestanden voldoende (sprekers, dossiernummers etc).
`sgdmeta` maakt deze informatie offline doorzoekbaar via een sqlite database.

Wil je alleen de data gebruiken, download dan de database van Zenodo (zie
[De data gebruiken](#de-data-gebruiken)). Zelf harvesten is alleen nodig om de
data te vernieuwen of na te gaan hoe hij tot stand kwam (zie
[De data opnieuw harvesten](#de-data-opnieuw-harvesten)).

## De data gebruiken

De data is gepubliceerd op Zenodo: <https://zenodo.org/records/22798123>. Deze data kan bekeken worden met elke SQLite-client, bijvoorbeeld `sqlite3 sgdmeta.sqlite3` of een applicatie met UI zoals `dbeaver`.

### De database

```sql
-- Waar sprak Wiegel over?
SELECT d.vergaderjaar, d.title
FROM document d JOIN spreker s USING (identifier)
WHERE s.naam = 'Wiegel';

-- Volledig-tekst zoeken in titels en beschrijvingen
SELECT title FROM document_fts WHERE document_fts MATCH 'kernenergie';

-- Alles rond een dossier
SELECT * FROM document WHERE dossiernummer = '9952';
```

Let op bij sprekers: de bron legt ze pas vanaf ongeveer 1866 vast. Een
Handelingendocument uit 1850 zonder sprekers betekent dus iets anders dan een
uit 1980. De tabel `sprekers_dekking` geeft per (type, vergaderjaar) hoeveel
documenten er zijn en in hoeveel daarvan sprekers zijn vastgelegd, zodat je
"zweeg" van "niet vastgelegd" kunt onderscheiden.

## De data opnieuw harvesten

Vereist [uv](https://docs.astral.sh/uv/)

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

## Ontwikkelen

```sh
uv run pytest        # tests draaien tegen echte API-responses in tests/fixtures/
uv run ruff check src tests
```

`HandleidingSRU2.0.pdf` is de handleiding van de SRU-API van repository.overheid.nl,
de bron waar `sgdmeta` de data vandaan haalt.

## Licentie

De code valt onder de [MIT-licentie](LICENSE). Voor de voorwaarden van de data zie
het [Zenodo-record](https://zenodo.org/records/22798123).
