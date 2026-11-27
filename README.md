# sgdmeta

Op officielebekendmakingen.nl staat de collectie Staten-Generaal Digitaal: de
parlementaire documenten van 1814 tot 1995, van belang voor parlementair historisch onderzoek.
De zoekfunctie is meer afgestemd om specifieke documenten te vinden
dan om een globaal beeld te krijgen van een periode.
Voor dergelijke vragen is de metadata van deze bestanden voldoende (sprekers, dossiernummers etc).
`sgdmeta` maakt deze informatie offline doorzoekbaar via een sqlite database.

De repo bestaat uit twee delen:

- [`app/`](app/README.md) — een programma met een eenvoudige gebruikersinterface
  (in de browser) om de database te downloaden, een selectie te maken en die naar
  Excel te exporteren. Geen SQL-kennis nodig.
- [`harvester/`](harvester/README.md) — de code die de metadata ophaalt bij de
  SRU-API van repository.overheid.nl en de database bouwt. Alleen nodig om de
  data te vernieuwen of na te gaan hoe hij tot stand kwam.

## De data gebruiken

De data is gepubliceerd op Zenodo: <https://zenodo.org/records/22798123>.

**Zonder SQL:** download de app van de
[releases-pagina](https://github.com/GliderGeek/sgdmeta/releases) en start hem; de
app haalt de database zelf op.

**Met SQL:** de database is te bekijken met elke SQLite-client, bijvoorbeeld
`sqlite3 sgdmeta.sqlite3` of een applicatie met UI zoals `dbeaver`.

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

De schemaversie staat in `PRAGMA user_version` (de eerste Zenodo-versie heeft
nog 0; dat is schema 1).

## Licentie

De code valt onder de [MIT-licentie](LICENSE). Voor de voorwaarden van de data zie
het [Zenodo-record](https://zenodo.org/records/22798123).
