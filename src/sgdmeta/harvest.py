"""
Fase 2 — rauwe XML naar schijf.

Deze fase verwerkt niets. Ze haalt pagina's op en schrijft de respons
onaangeraakt weg, gzip-gecomprimeerd, één bestand per pagina.
"""

from __future__ import annotations

import gzip
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from .partition import Partition, Plan
from .sru import MAX_RECORDS, Sru


@dataclass(frozen=True, slots=True)
class Page:
    """Eén op te halen pagina."""

    partition: Partition
    start: int

    def path(self, root: Path) -> Path:
        # Startpositie in de naam, links opgevuld, zodat een directorylisting
        # meteen in de juiste volgorde staat.
        return root / self.partition.key / f"{self.start:08d}.xml.gz"


def plan_pages(plan: Plan) -> list[Page]:
    """Alle pagina's die samen de collectie vormen."""
    return [
        Page(partition=p, start=1 + i * MAX_RECORDS)
        for p in plan.partitions
        for i in range(p.pages)
    ]


def missing_pages(pages: list[Page], root: Path) -> list[Page]:
    """De pagina's die nog niet op schijf staan."""
    return [p for p in pages if not p.path(root).exists()]


async def fetch_page(sru: Sru, page: Page, root: Path) -> int:
    """Haal één pagina op en schrijf hem weg. Retourneert het aantal bytes."""
    raw = await sru.search(page.partition.query, start=page.start, maximum=MAX_RECORDS)

    target = page.path(root)
    target.parent.mkdir(parents=True, exist_ok=True)
    tmp = target.with_suffix(".tmp")
    tmp.write_bytes(gzip.compress(raw))
    tmp.replace(target)
    return len(raw)


async def harvest(
    sru: Sru,
    plan: Plan,
    root: Path,
    *,
    on_page: Callable[[Page, int], None] | None = None,
) -> int:
    """Haal alle ontbrekende pagina's op. Retourneert het aantal opgehaald."""
    todo = missing_pages(plan_pages(plan), root)
    for page in todo:
        size = await fetch_page(sru, page, root)
        if on_page:
            on_page(page, size)
    return len(todo)


def iter_raw(root: Path):
    """Alle opgehaalde pagina's, in volgorde. Voor fase 3."""
    for partition_dir in sorted(p for p in root.iterdir() if p.is_dir()):
        for path in sorted(partition_dir.glob("*.xml.gz")):
            yield path, gzip.decompress(path.read_bytes())
