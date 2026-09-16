"""Fase 3 — de rauwe XML omzetten in een doorzoekbare SQLite-database.

Draait volledig lokaal op wat fase 2 heeft opgehaald. Geen requests.

Deze module vult de tabel `sprekers_dekking`: per (type, vergaderjaar)
hoeveel documenten er zijn en in hoeveel daarvan sprekers zijn vastgelegd.
De bron legt vóór ongeveer 1866 geen sprekers vast. Een Handelingendocument
uit 1850 zonder sprekers betekent dus iets fundamenteel anders dan een
Handelingendocument uit 1980 zonder sprekers. 
"""

from __future__ import annotations

import sqlite3
import xml.etree.ElementTree as ET
from collections.abc import Callable, Iterator
from dataclasses import dataclass, field
from pathlib import Path

from .sru import NS

SCHEMA = """
CREATE TABLE IF NOT EXISTS document (
    identifier        TEXT PRIMARY KEY,
    doctype           TEXT,
    title             TEXT,
    creator           TEXT,
    vergaderjaar      TEXT,
    datum             TEXT,   -- dcterms:temporal / date: waar het document over gaat
    datum_vergadering TEXT,
    dossiernummer     TEXT,
    ondernummer       TEXT,
    startpagina       INTEGER,
    aantalpaginas     INTEGER,
    bron_identifier   TEXT,
    content_area      TEXT,
    beschrijving      TEXT,
    preferred_url     TEXT
);

CREATE TABLE IF NOT EXISTS spreker (
    identifier TEXT NOT NULL REFERENCES document(identifier) ON DELETE CASCADE,
    naam       TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS ondertekening (
    identifier TEXT NOT NULL REFERENCES document(identifier) ON DELETE CASCADE,
    naam       TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS behandeld_dossier (
    identifier TEXT NOT NULL REFERENCES document(identifier) ON DELETE CASCADE,
    nummer     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS item_url (
    identifier    TEXT NOT NULL REFERENCES document(identifier) ON DELETE CASCADE,
    manifestation TEXT NOT NULL,
    url           TEXT NOT NULL
);

-- Per (type, vergaderjaar): is het ontbreken van sprekers betekenisvol?
CREATE TABLE IF NOT EXISTS sprekers_dekking (
    doctype      TEXT NOT NULL,
    vergaderjaar TEXT NOT NULL,
    documenten   INTEGER NOT NULL,
    met_sprekers INTEGER NOT NULL,
    PRIMARY KEY (doctype, vergaderjaar)
);

CREATE VIRTUAL TABLE IF NOT EXISTS document_fts USING fts5(
    title, beschrijving,
    content='document', content_rowid='rowid', tokenize='unicode61'
);
"""

# Bewust niet in SCHEMA: indexen bijhouden tijdens miljoenen inserts is een stuk
# duurder dan ze achteraf in één keer bouwen. rebuild_derived() maakt ze aan.
INDEXES = """
CREATE INDEX IF NOT EXISTS ix_doc_jaar      ON document(vergaderjaar);
CREATE INDEX IF NOT EXISTS ix_doc_type      ON document(doctype);
CREATE INDEX IF NOT EXISTS ix_doc_dossier   ON document(dossiernummer);
CREATE INDEX IF NOT EXISTS ix_spreker       ON spreker(naam);
CREATE INDEX IF NOT EXISTS ix_spreker_doc   ON spreker(identifier);
CREATE INDEX IF NOT EXISTS ix_ondert        ON ondertekening(naam);
CREATE INDEX IF NOT EXISTS ix_ondert_doc    ON ondertekening(identifier);
CREATE INDEX IF NOT EXISTS ix_behandeld     ON behandeld_dossier(nummer);
CREATE INDEX IF NOT EXISTS ix_behandeld_doc ON behandeld_dossier(identifier);
CREATE INDEX IF NOT EXISTS ix_itemurl_doc   ON item_url(identifier);
"""

# Alles wat store() vult, in de volgorde waarin je het leeg mag maken.
TABLES = ("spreker", "ondertekening", "behandeld_dossier", "item_url", "document")

# De kindtabellen, met een verwijzing naar hun document.
CHILD_TABLES = TABLES[:-1]


@dataclass(slots=True)
class Document:
    identifier: str
    doctype: str | None = None
    title: str | None = None
    creator: str | None = None
    vergaderjaar: str | None = None
    datum: str | None = None
    datum_vergadering: str | None = None
    dossiernummer: str | None = None
    ondernummer: str | None = None
    startpagina: int | None = None
    aantalpaginas: int | None = None
    bron_identifier: str | None = None
    content_area: str | None = None
    beschrijving: str | None = None
    preferred_url: str | None = None
    sprekers: list[str] = field(default_factory=list)
    ondertekenaars: list[str] = field(default_factory=list)
    dossiers: list[str] = field(default_factory=list)
    item_urls: list[tuple[str, str]] = field(default_factory=list)


def _text(el: ET.Element | None) -> str | None:
    if el is None or el.text is None:
        return None
    value = el.text.strip()
    return value or None


def _int(el: ET.Element | None) -> int | None:
    value = _text(el)
    if value is None:
        return None
    try:
        return int(value)
    except ValueError:
        return None


def _all(parent: ET.Element, path: str) -> list[str]:
    return [v for v in (_text(e) for e in parent.findall(path, NS)) if v]


def parse_record(record: ET.Element) -> Document | None:
    """Zet één <sru:record> om in een Document."""
    kern = record.find(".//ow:owmskern", NS)
    identifier = _text(kern.find("dcterms:identifier", NS)) if kern is not None else None
    if not identifier:
        return None

    doc = Document(identifier=identifier)
    if kern is not None:
        doc.title = _text(kern.find("dcterms:title", NS))
        doc.doctype = _text(kern.find("dcterms:type", NS))
        doc.creator = _text(kern.find("dcterms:creator", NS))
        doc.datum = _text(kern.find("dcterms:temporal", NS))

    mantel = record.find(".//ow:owmsmantel", NS)
    if mantel is not None:
        # Meerdere beschrijvingen komen voor; bewaar ze alle, gescheiden.
        beschrijvingen = _all(mantel, "dcterms:description")
        doc.beschrijving = "\n".join(beschrijvingen) or None
        if doc.datum is None:
            doc.datum = _text(mantel.find("dcterms:date", NS))

    tp = record.find(".//ow:tpmeta", NS)
    if tp is not None:
        doc.content_area = _text(tp.find("c:content-area", NS))
        doc.vergaderjaar = _text(tp.find("ow:vergaderjaar", NS))
        doc.datum_vergadering = _text(tp.find("ow:datumVergadering", NS))
        doc.dossiernummer = _text(tp.find("ow:dossiernummer", NS))
        doc.ondernummer = _text(tp.find("ow:ondernummer", NS))
        doc.startpagina = _int(tp.find("ow:startpagina", NS))
        doc.aantalpaginas = _int(tp.find("ow:aantalpaginas", NS))
        doc.bron_identifier = _text(tp.find("ow:bronIdentifier", NS))
        doc.sprekers = _all(tp, "ow:spreker")
        doc.ondertekenaars = _all(tp, "ow:ondertekening")
        doc.dossiers = _all(tp, "ow:behandeldDossier")

    enriched = record.find(".//gzd:enrichedData", NS)
    if enriched is not None:
        doc.preferred_url = _text(enriched.find("gzd:preferredUrl", NS))
        for item in enriched.findall("gzd:itemUrl", NS):
            url = _text(item)
            manifestation = item.get("manifestation")
            if url and manifestation:
                doc.item_urls.append((manifestation, url))

    return doc


def parse_page(raw: bytes) -> Iterator[Document]:
    """Alle documenten uit één opgehaalde pagina."""
    root = ET.fromstring(raw)
    for record in root.findall(".//sru:record", NS):
        doc = parse_record(record)
        if doc is not None:
            yield doc


def connect(path: Path) -> sqlite3.Connection:
    path.parent.mkdir(parents=True, exist_ok=True)
    con = sqlite3.connect(path)
    con.execute("PRAGMA journal_mode=WAL")
    con.execute("PRAGMA synchronous=NORMAL")
    con.execute("PRAGMA foreign_keys=ON")
    con.executescript(SCHEMA)
    return con


def store(con: sqlite3.Connection, docs: list[Document], *, replace: bool = True) -> None:
    """Schrijf documenten weg. Idempotent: opnieuw indexeren mag.

    De kindtabellen hebben geen unieke sleutel, dus opnieuw wegschrijven zou
    rijen verdubbelen. Daarom gaan de oude er eerst uit.

    Met ``replace=False`` blijft dat opruimen achterwege. Dat mag alleen als je
    zeker weet dat deze documenten er nog niet in staan — `build()` maakt de
    tabellen vooraf leeg en weet dat dus. Het scheelt één DELETE per document
    per tabel, wat bij een volle herbouw honderdduizenden statements is.
    """
    if replace:
        ids = [(d.identifier,) for d in docs]
        for table in CHILD_TABLES:
            con.executemany(f"DELETE FROM {table} WHERE identifier = ?", ids)

    con.executemany(
        """INSERT INTO document (identifier, doctype, title, creator, vergaderjaar,
                datum, datum_vergadering, dossiernummer, ondernummer, startpagina,
                aantalpaginas, bron_identifier, content_area, beschrijving, preferred_url)
           VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
           ON CONFLICT(identifier) DO UPDATE SET
                doctype=excluded.doctype, title=excluded.title, creator=excluded.creator,
                vergaderjaar=excluded.vergaderjaar, datum=excluded.datum,
                datum_vergadering=excluded.datum_vergadering,
                dossiernummer=excluded.dossiernummer, ondernummer=excluded.ondernummer,
                startpagina=excluded.startpagina, aantalpaginas=excluded.aantalpaginas,
                bron_identifier=excluded.bron_identifier, content_area=excluded.content_area,
                beschrijving=excluded.beschrijving, preferred_url=excluded.preferred_url""",
        [
            (
                d.identifier, d.doctype, d.title, d.creator, d.vergaderjaar,
                d.datum, d.datum_vergadering, d.dossiernummer, d.ondernummer,
                d.startpagina, d.aantalpaginas, d.bron_identifier, d.content_area,
                d.beschrijving, d.preferred_url,
            )
            for d in docs
        ],
    )

    con.executemany(
        "INSERT INTO spreker (identifier, naam) VALUES (?,?)",
        [(d.identifier, n) for d in docs for n in d.sprekers],
    )
    con.executemany(
        "INSERT INTO ondertekening (identifier, naam) VALUES (?,?)",
        [(d.identifier, n) for d in docs for n in d.ondertekenaars],
    )
    con.executemany(
        "INSERT INTO behandeld_dossier (identifier, nummer) VALUES (?,?)",
        [(d.identifier, n) for d in docs for n in d.dossiers],
    )
    con.executemany(
        "INSERT INTO item_url (identifier, manifestation, url) VALUES (?,?,?)",
        [(d.identifier, m, u) for d in docs for m, u in d.item_urls],
    )


def create_indexes(con: sqlite3.Connection) -> None:
    """Leg de zoekindexen aan. Na het laden, niet ervoor — zie INDEXES."""
    con.executescript(INDEXES)


def rebuild_derived(
    con: sqlite3.Connection, *, on_stage: Callable[[str], None] | None = None
) -> None:
    """Herbouw indexen, FTS en sprekersdekking.

    Dit is het werk ná de laatste pagina. Bij een volle collectie duurt het
    minuten, vandaar `on_stage`: zonder terugkoppeling lijkt het programma
    te hangen op het moment dat de voortgangsbalk net vol is.
    """
    if on_stage:
        on_stage("indexen aanleggen")
    create_indexes(con)

    if on_stage:
        on_stage("FTS-index bouwen")
    con.execute("INSERT INTO document_fts(document_fts) VALUES('rebuild')")

    if on_stage:
        on_stage("sprekersdekking berekenen")
    con.execute("DELETE FROM sprekers_dekking")
    con.execute(
        """INSERT INTO sprekers_dekking (doctype, vergaderjaar, documenten, met_sprekers)
           SELECT d.doctype,
                  COALESCE(d.vergaderjaar, ''),
                  COUNT(*),
                  SUM(CASE WHEN EXISTS (
                      SELECT 1 FROM spreker s WHERE s.identifier = d.identifier
                  ) THEN 1 ELSE 0 END)
           FROM document d
           GROUP BY d.doctype, COALESCE(d.vergaderjaar, '')"""
    )
    con.commit()


COMMIT_EVERY = 25


def build(
    raw_root: Path,
    db_path: Path,
    *,
    on_page: Callable[[Path, int], None] | None = None,
    on_stage: Callable[[str], None] | None = None,
) -> int:
    """Lees alle opgehaalde pagina's en vul de database. Retourneert #documenten.

    Herbouwt de database volledig: de tabellen gaan eerst leeg.
    """
    from .harvest import iter_raw

    con = connect(db_path)
    total = 0
    try:
        # Tijdens de herbouw is er niets te beschermen: klapt hij, dan draai je
        # hem opnieuw. Duurzaamheid per transactie is die vertraging niet waard.
        con.execute("PRAGMA synchronous=OFF")
        con.execute("PRAGMA foreign_keys=OFF")
        con.execute("PRAGMA cache_size=-200000")
        con.execute("PRAGMA temp_store=MEMORY")

        for table in TABLES:
            con.execute(f"DELETE FROM {table}")
        con.execute("DELETE FROM sprekers_dekking")
        con.commit()

        for n, (path, raw) in enumerate(iter_raw(raw_root), start=1):
            docs = list(parse_page(raw))
            # De tabellen waren leeg en elk document komt in precies één
            # pagina voor, dus er valt niets te vervangen.
            store(con, docs, replace=False)
            if n % COMMIT_EVERY == 0:
                con.commit()
            total += len(docs)
            if on_page:
                on_page(path, len(docs))
        con.commit()

        rebuild_derived(con, on_stage=on_stage)
        con.execute("PRAGMA synchronous=NORMAL")
        con.execute("PRAGMA foreign_keys=ON")
    finally:
        con.close()
    return total
