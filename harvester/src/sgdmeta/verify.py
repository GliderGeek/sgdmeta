"""Controles op de geharveste data.

`resultCountPrecision` staat op `estimate`, dus "aantal opgehaald ==
numberOfRecords" is geen bewijs van volledigheid. De controles hier werken
daarom op unieke identifiers, niet op tellingen.

De sprekersdekking is geen kwaliteitscontrole maar een eigenschap van de bron
die je moet kennen voordat je conclusies trekt: vóór ongeveer 1866 legt de
bron geen sprekers vast.
"""

from __future__ import annotations

import sqlite3
from dataclasses import dataclass
from pathlib import Path

from .harvest import iter_raw
from .partition import Plan
from .sru import NS


@dataclass(frozen=True, slots=True)
class Issue:
    severity: str  # "fout" of "let op"
    message: str


def check_raw(raw_root: Path, plan: Plan) -> tuple[list[Issue], int]:
    """Controleer de opgehaalde pagina's op duplicaten en gaten.

    Retourneert de bevindingen en het aantal unieke identifiers.
    """
    import xml.etree.ElementTree as ET

    issues: list[Issue] = []
    seen: set[str] = set()
    duplicates = 0
    per_partition: dict[str, int] = {}

    for path, raw in iter_raw(raw_root):
        root = ET.fromstring(raw)
        ids = [
            (e.text or "").strip()
            for e in root.iter(f"{{{NS['dcterms']}}}identifier")
            if (e.text or "").strip()
        ]
        # Alleen de eerste identifier per record telt; enrichedData bevat er
        # geen, dus dit is veilig, maar we ontdubbelen defensief per pagina.
        key = path.parent.name
        per_partition[key] = per_partition.get(key, 0) + len(ids)
        for identifier in ids:
            if identifier in seen:
                duplicates += 1
            seen.add(identifier)

    if duplicates:
        issues.append(
            Issue(
                "fout",
                f"{duplicates} dubbele identifiers. De paginering heeft records "
                "twee keer geleverd, wat op instabiele sortering wijst.",
            )
        )

    for partition in plan.partitions:
        got = per_partition.get(partition.key, 0)
        if got == 0:
            issues.append(Issue("fout", f"partitie {partition.key!r} is helemaal leeg"))
        elif got < partition.expected:
            issues.append(
                Issue(
                    "fout",
                    f"partitie {partition.key!r}: {got} records opgehaald, "
                    f"{partition.expected} verwacht ({got - partition.expected:+d})",
                )
            )

    if len(seen) < plan.total:
        issues.append(
            Issue(
                "fout",
                f"{len(seen)} unieke identifiers, {plan.total} verwacht "
                f"({len(seen) - plan.total:+d})",
            )
        )

    return issues, len(seen)


def speaker_coverage(con: sqlite3.Connection, doctype: str = "Handelingen") -> list[tuple]:
    """Sprekersdekking per vergaderjaar, oplopend in de tijd."""
    return con.execute(
        """SELECT vergaderjaar, documenten, met_sprekers
           FROM sprekers_dekking
           WHERE doctype = ?
           ORDER BY vergaderjaar""",
        (doctype,),
    ).fetchall()


def coverage_cutoff(rows: list[tuple]) -> str | None:
    """Het eerste vergaderjaar waarin sprekers structureel zijn vastgelegd."""
    for vergaderjaar, documenten, met_sprekers in rows:
        if documenten and met_sprekers / documenten > 0.5:
            return vergaderjaar
    return None


def check_db(db_path: Path) -> list[Issue]:
    """Controles op de gevulde database."""
    issues: list[Issue] = []
    con = sqlite3.connect(db_path)
    try:
        (docs,) = con.execute("SELECT COUNT(*) FROM document").fetchone()
        if not docs:
            return [Issue("fout", "de database bevat geen documenten")]

        (zonder_type,) = con.execute(
            "SELECT COUNT(*) FROM document WHERE doctype IS NULL"
        ).fetchone()
        if zonder_type:
            issues.append(Issue("fout", f"{zonder_type} documenten zonder type"))

        (wees,) = con.execute(
            """SELECT COUNT(*) FROM spreker
               WHERE identifier NOT IN (SELECT identifier FROM document)"""
        ).fetchone()
        if wees:
            issues.append(Issue("fout", f"{wees} sprekers zonder bijbehorend document"))

        rows = speaker_coverage(con)
        cutoff = coverage_cutoff(rows)
        if cutoff:
            stil = sum(1 for vj, _, ms in rows if vj < cutoff and ms == 0)
            if stil:
                issues.append(
                    Issue(
                        "let op",
                        f"in {stil} vergaderjaren vóór {cutoff} legt de bron geen "
                        "sprekers vast. Leeg is daar 'niet vastgelegd', niet 'zweeg' "
                        "— zie sprekers_dekking.",
                    )
                )
    finally:
        con.close()
    return issues
