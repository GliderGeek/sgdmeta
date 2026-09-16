"""Tests tegen echte responses van de SRU-API.

De fixtures zijn onbewerkte responses, opgehaald tijdens het onderzoek. Ze
staan er zodat de parser getest kan worden zonder de server aan te raken.
"""

from pathlib import Path

import pytest

from sgdmeta.index import parse_page
from sgdmeta.sru import MAX_RECORDS, SruError, parse_count

FIXTURES = Path(__file__).parent / "fixtures"


def load(name: str) -> bytes:
    return (FIXTURES / name).read_bytes()


def test_diagnostic_wordt_een_fout():
    """Een diagnostic mag niet als lege resultatenlijst doorgaan.

    Deze fixture is de respons op maximumRecords=2000. Zonder controle zou
    de harvester hem als 'nul records' interpreteren en stilletjes een gat
    in de data achterlaten.
    """
    with pytest.raises(SruError, match="maximumRecords is 1000"):
        parse_count(load("diagnostic.xml"))


def test_sprekers_komen_uit_de_metadata():
    """De kernaanname van het hele project."""
    docs = list(parse_page(load("sgd_handelingen.xml")))
    assert len(docs) == 1

    doc = docs[0]
    assert doc.identifier == "0000223830"
    assert doc.doctype == "Handelingen"
    assert doc.vergaderjaar == "1970-1971"
    assert doc.datum_vergadering == "1971-01-19"

    # 29 sprekers, rechtstreeks uit de SRU-respons: geen extra request nodig.
    assert len(doc.sprekers) == 29
    assert "Wiegel" in doc.sprekers
    assert "van Thiel" in doc.sprekers


def test_handelingen_hebben_dossiers_en_paginering():
    doc = next(iter(parse_page(load("sgd_handelingen.xml"))))
    assert "10900" in doc.dossiers
    assert len(doc.dossiers) == 16
    assert doc.startpagina == 2287
    assert doc.aantalpaginas == 46
    assert doc.bron_identifier == "sgd:19701971:0000639"


def test_item_urls_blijven_bewaard():
    """De routes naar pdf/ocr/jpg, zodat fulltext later kan zonder herharvest."""
    doc = next(iter(parse_page(load("sgd_handelingen.xml"))))
    manifestations = {m for m, _ in doc.item_urls}
    assert {"pdf", "ocr", "jpg"} <= manifestations
    assert doc.preferred_url == "https://zoek.officielebekendmakingen.nl/0000223830"


def test_naamlijst_wordt_gewoon_geparsed():
    """Het type dat uit de handgeschreven lijst viel.

    Naamlijst heeft geen sprekers en geen dossiernummer. Dat mag de parser
    niet laten struikelen, want dan zou het type alsnog wegvallen.
    """
    docs = list(parse_page(load("sgd_naamlijst.xml")))
    assert len(docs) == 20
    assert {d.doctype for d in docs} == {"Naamlijst"}
    assert all(d.identifier for d in docs)
    assert all(d.title for d in docs)


def test_max_records_grens_wordt_lokaal_bewaakt():
    """Voorkom de request die de API toch zou weigeren."""
    assert MAX_RECORDS == 1000
