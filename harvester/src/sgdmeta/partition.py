"""Fase 1 — de werklijst afleiden uit de API.

De collectie wordt opgedeeld in één partitie per documenttype. 
"""

from __future__ import annotations

import json
import xml.etree.ElementTree as ET
from dataclasses import asdict, dataclass
from pathlib import Path

from .sru import MAX_RECORDS, NS, Sru, cql_quote

SGD = "c.product-area==sgd"


@dataclass(frozen=True, slots=True)
class Partition:
    """Een disjuncte deelverzameling van de collectie."""

    doctype: str
    expected: int

    @property
    def query(self) -> str:
        return f"{SGD} AND dt.type=={cql_quote(self.doctype)}"

    @property
    def key(self) -> str:
        """Bestandsnaamveilige sleutel."""
        return "".join(c if c.isalnum() else "-" for c in self.doctype).strip("-")

    @property
    def pages(self) -> int:
        return -(-self.expected // MAX_RECORDS)


@dataclass(frozen=True, slots=True)
class Plan:
    """De werklijst, plus de getallen om hem te controleren."""

    total: int
    partitions: list[Partition]

    @property
    def covered(self) -> int:
        return sum(p.expected for p in self.partitions)

    @property
    def pages(self) -> int:
        """Aantal requests dat het harvesten gaat kosten."""
        return sum(p.pages for p in self.partitions)


async def discover_doctypes(sru: Sru, total: int) -> list[str]:
    """Haal de typelijst uit de data."""
    samples = 12
    offsets = [1 + round(i * max(0, total - 1) / max(1, samples - 1)) for i in range(samples)]

    found: set[str] = set()
    for offset in offsets:
        raw = await sru.search(SGD, start=offset, maximum=200)
        root = ET.fromstring(raw)
        found |= {
            (e.text or "").strip()
            for e in root.iter(f"{{{NS['dcterms']}}}type")
            if (e.text or "").strip()
        }
    return sorted(found)


async def residual_count(sru: Sru, doctypes: list[str]) -> int:
    """Records die buiten de gevonden types vallen. Moet 0 zijn."""
    clauses = " OR ".join(f"dt.type=={cql_quote(t)}" for t in doctypes)
    return await sru.count(f"{SGD} NOT ({clauses})")


async def build_plan(sru: Sru) -> Plan:
    """Stel de werklijst samen en bewijs dat hij de collectie dekt."""
    total = await sru.count(SGD)
    doctypes = await discover_doctypes(sru, total)

    residual = await residual_count(sru, doctypes)
    if residual:
        raise RuntimeError(
            f"{residual} records vallen buiten de gevonden types {doctypes}. "
            "De typeafleiding is incompleet; harvesten zou records missen."
        )

    counts = []
    for doctype in doctypes:
        counts.append(await sru.count(f"{SGD} AND dt.type=={cql_quote(doctype)}"))

    partitions = [
        Partition(doctype=t, expected=n) for t, n in zip(doctypes, counts, strict=True) if n
    ]
    plan = Plan(total=total, partitions=partitions)

    # Sluitcontrole
    if plan.covered != plan.total:
        raise RuntimeError(
            f"werklijst sluit niet: {plan.covered} gedekt van {plan.total} "
            f"({plan.total - plan.covered:+d})"
        )
    return plan


def save_plan(plan: Plan, path: Path) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    payload = {"total": plan.total, "partitions": [asdict(p) for p in plan.partitions]}
    path.write_text(json.dumps(payload, indent=2, ensure_ascii=False), encoding="utf-8")


def load_plan(path: Path) -> Plan:
    payload = json.loads(path.read_text(encoding="utf-8"))
    return Plan(
        total=payload["total"],
        partitions=[Partition(**p) for p in payload["partitions"]],
    )
