"""Tests op het opslaan en het sprekersdekking-model."""

from pathlib import Path

from sgdmeta.index import (
    SCHEMA_VERSION,
    Document,
    connect,
    parse_page,
    rebuild_derived,
    store,
)
from sgdmeta.verify import coverage_cutoff, speaker_coverage

FIXTURES = Path(__file__).parent / "fixtures"


def test_opslaan_is_idempotent(tmp_path: Path):
    """Opnieuw indexeren mag geen dubbele sprekers opleveren."""
    raw = (FIXTURES / "sgd_handelingen.xml").read_bytes()
    docs = list(parse_page(raw))

    con = connect(tmp_path / "t.sqlite3")
    try:
        store(con, docs)
        store(con, docs)  # tweede keer
        con.commit()

        assert con.execute("SELECT COUNT(*) FROM document").fetchone()[0] == 1
        assert con.execute("SELECT COUNT(*) FROM spreker").fetchone()[0] == 29
    finally:
        con.close()


def test_fts_vindt_documenten(tmp_path: Path):
    raw = (FIXTURES / "sgd_handelingen.xml").read_bytes()
    con = connect(tmp_path / "t.sqlite3")
    try:
        store(con, list(parse_page(raw)))
        rebuild_derived(con)
        hits = con.execute(
            "SELECT title FROM document_fts WHERE document_fts MATCH 'Handelingen'"
        ).fetchall()
        assert hits
    finally:
        con.close()


def test_schemaversie_na_rebuild(tmp_path: Path):
    """Lezers van de database herkennen hem aan `user_version`."""
    con = connect(tmp_path / "t.sqlite3")
    try:
        assert con.execute("PRAGMA user_version").fetchone()[0] == 0
        rebuild_derived(con)
        assert con.execute("PRAGMA user_version").fetchone()[0] == SCHEMA_VERSION
    finally:
        con.close()


def test_dekking_onderscheidt_zweeg_van_niet_vastgelegd(tmp_path: Path):
    """De kern van het datamodel rond sprekers.

    Twee vergaderjaren met evenveel documenten: in het ene legt de bron
    sprekers vast, in het andere niet. Een analyse moet die twee uit elkaar
    kunnen houden, anders lijkt de negentiende eeuw stil.
    """
    oud = [
        Document(identifier=f"oud-{i}", doctype="Handelingen", vergaderjaar="1850-1851")
        for i in range(5)
    ]
    nieuw = [
        Document(
            identifier=f"nieuw-{i}",
            doctype="Handelingen",
            vergaderjaar="1980-1981",
            sprekers=["Wiegel"],
        )
        for i in range(5)
    ]

    con = connect(tmp_path / "t.sqlite3")
    try:
        store(con, oud + nieuw)
        rebuild_derived(con)

        rows = speaker_coverage(con)
        dekking = {vj: (docs, met) for vj, docs, met in rows}

        assert dekking["1850-1851"] == (5, 0)  # niet vastgelegd
        assert dekking["1980-1981"] == (5, 5)  # wel vastgelegd

        # De cutoff is het eerste jaar waarin het structureel gebeurt.
        assert coverage_cutoff(rows) == "1980-1981"
    finally:
        con.close()
