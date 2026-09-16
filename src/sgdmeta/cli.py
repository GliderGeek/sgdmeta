"""Commandoregel voor sgdmeta.

    sgdmeta plan      # fase 1: werklijst afleiden en controleren
    sgdmeta harvest   # fase 2: rauwe XML ophalen (hervatbaar)
    sgdmeta index     # fase 3: lokaal parsen naar SQLite
    sgdmeta verify    # controles op ruwe data en database
    sgdmeta status    # wat staat er al?
"""

from __future__ import annotations

import argparse
import asyncio
import sqlite3
import sys
from pathlib import Path

from rich.console import Console
from rich.progress import (
    BarColumn,
    MofNCompleteColumn,
    Progress,
    TextColumn,
    TimeRemainingColumn,
)
from rich.table import Table

from . import harvest as harvest_mod
from . import index as index_mod
from . import partition as partition_mod
from . import verify as verify_mod
from .sru import Sru

console = Console()

DATA = Path("data")
PLAN_PATH = DATA / "plan.json"
RAW_ROOT = DATA / "raw"
DB_PATH = DATA / "sgdmeta.sqlite3"


def _progress() -> Progress:
    return Progress(
        TextColumn("[bold blue]{task.description}"),
        BarColumn(),
        MofNCompleteColumn(),
        TimeRemainingColumn(elapsed_when_finished=True),
        console=console,
    )


async def cmd_plan(args: argparse.Namespace) -> int:
    async with Sru(min_interval=args.interval) as sru:
        with console.status("werklijst afleiden uit de API..."):
            plan = await partition_mod.build_plan(sru)

    partition_mod.save_plan(plan, args.plan)

    table = Table(title="Werklijst", show_footer=True)
    table.add_column("Type", footer="totaal")
    table.add_column("Records", justify="right", footer=f"{plan.covered:,}")
    table.add_column("Pagina's", justify="right", footer=f"{plan.pages:,}")
    for p in sorted(plan.partitions, key=lambda p: -p.expected):
        table.add_row(p.doctype, f"{p.expected:,}", f"{p.pages:,}")
    console.print(table)

    eta = plan.pages * args.interval / 60
    console.print(
        f"Werklijst sluit: {plan.covered:,} van {plan.total:,} records.\n"
        f"Harvesten kost {plan.pages:,} requests, ongeveer {eta:.0f} minuten "
        f"bij {args.interval:g}s per request."
    )
    console.print(f"Opgeslagen in [bold]{args.plan}[/bold]")
    return 0


async def cmd_harvest(args: argparse.Namespace) -> int:
    if not args.plan.exists():
        console.print("[red]Geen werklijst. Draai eerst 'sgdmeta plan'.[/red]")
        return 1

    plan = partition_mod.load_plan(args.plan)
    pages = harvest_mod.plan_pages(plan)
    todo = harvest_mod.missing_pages(pages, args.raw)

    done = len(pages) - len(todo)
    if not todo:
        console.print(f"Alle {len(pages):,} pagina's staan al op schijf.")
        return 0

    console.print(
        f"{len(todo):,} pagina's te gaan van {len(pages):,}"
        + (f" ({done:,} al opgehaald)" if done else "")
        + f", ongeveer {len(todo) * args.interval / 60:.0f} minuten."
    )

    async with Sru(min_interval=args.interval) as sru:
        with _progress() as progress:
            task = progress.add_task("harvesten", total=len(todo))

            def on_page(page: harvest_mod.Page, size: int) -> None:
                progress.update(
                    task, advance=1, description=f"harvesten {page.partition.key}"
                )

            try:
                await harvest_mod.harvest(sru, plan, args.raw, on_page=on_page)
            except KeyboardInterrupt:
                console.print("\n[yellow]Onderbroken. Draai opnieuw om te hervatten.[/yellow]")
                return 130

    console.print("[green]Harvest compleet.[/green] Draai nu 'sgdmeta index'.")
    return 0


def cmd_index(args: argparse.Namespace) -> int:
    if not args.raw.exists():
        console.print("[red]Geen ruwe data. Draai eerst 'sgdmeta harvest'.[/red]")
        return 1

    n_pages = sum(1 for _ in args.raw.rglob("*.xml.gz"))

    progress = _progress()
    task = progress.add_task("indexeren", total=n_pages)
    status = console.status("")

    def on_stage(name: str) -> None:
        progress.stop()  # geen effect als hij al stilstaat
        status.update(f"{name}...")
        status.start()

    progress.start()
    try:
        total = index_mod.build(
            args.raw,
            args.db,
            on_page=lambda path, n: progress.update(task, advance=1),
            on_stage=on_stage,
        )
    finally:
        progress.stop()
        status.stop()

    console.print(f"[green]{total:,} documenten geïndexeerd[/green] in {args.db}")
    return 0


def cmd_verify(args: argparse.Namespace) -> int:
    issues: list[verify_mod.Issue] = []

    if args.plan.exists() and args.raw.exists():
        plan = partition_mod.load_plan(args.plan)
        with console.status("ruwe pagina's controleren op duplicaten en gaten..."):
            raw_issues, unique = verify_mod.check_raw(args.raw, plan)
        issues += raw_issues
        console.print(
            f"{unique:,} unieke identifiers in de ruwe data (verwacht {plan.total:,})"
        )

    if args.db.exists():
        issues += verify_mod.check_db(args.db)

        con = sqlite3.connect(args.db)
        try:
            rows = verify_mod.speaker_coverage(con)
        finally:
            con.close()

        if rows:
            cutoff = verify_mod.coverage_cutoff(rows)
            table = Table(title="Sprekersdekking, Handelingen")
            table.add_column("Vergaderjaar")
            table.add_column("Documenten", justify="right")
            table.add_column("Met sprekers", justify="right")
            table.add_column("Dekking", justify="right")
            # Toon de randen: de eerste jaren en het eind, niet alle 180.
            selection = rows[:8] + ([("...", "", "")] if len(rows) > 16 else []) + rows[-8:]
            for row in selection:
                if row[0] == "...":
                    table.add_row("...", "", "", "")
                    continue
                vj, docs, met = row
                pct = f"{met / docs:.0%}" if docs else "-"
                table.add_row(vj, f"{docs:,}", f"{met:,}", pct)
            console.print(table)
            if cutoff:
                console.print(f"Sprekers structureel vastgelegd vanaf [bold]{cutoff}[/bold].")

    if not issues:
        console.print("[green]Geen problemen gevonden.[/green]")
        return 0

    for issue in issues:
        colour = "red" if issue.severity == "fout" else "yellow"
        console.print(f"[{colour}]{issue.severity}:[/{colour}] {issue.message}")
    return 1 if any(i.severity == "fout" for i in issues) else 0


def cmd_status(args: argparse.Namespace) -> int:
    console.print(f"werklijst : {args.plan if args.plan.exists() else '—'}")

    if args.plan.exists():
        plan = partition_mod.load_plan(args.plan)
        pages = harvest_mod.plan_pages(plan)
        have = len(pages) - len(harvest_mod.missing_pages(pages, args.raw))
        console.print(f"ruwe data : {have:,} / {len(pages):,} pagina's")
    elif args.raw.exists():
        console.print(f"ruwe data : {sum(1 for _ in args.raw.rglob('*.xml.gz')):,} pagina's")

    if args.db.exists():
        con = sqlite3.connect(args.db)
        try:
            docs = con.execute("SELECT COUNT(*) FROM document").fetchone()[0]
            sprekers = con.execute("SELECT COUNT(DISTINCT naam) FROM spreker").fetchone()[0]
            console.print(f"database  : {docs:,} documenten, {sprekers:,} unieke sprekers")
        finally:
            con.close()
    else:
        console.print("database  : —")
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="sgdmeta", description=__doc__)
    parser.add_argument("--plan", type=Path, default=PLAN_PATH)
    parser.add_argument("--raw", type=Path, default=RAW_ROOT)
    parser.add_argument("--db", type=Path, default=DB_PATH)
    parser.add_argument(
        "--interval",
        type=float,
        default=5.0,
        metavar="SEC",
        help="minimale tijd tussen requests (standaard 5s; de server "
        "rate-limit met 429 bij hogere tempo's)",
    )

    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("plan", help="fase 1: werklijst afleiden")
    sub.add_parser("harvest", help="fase 2: rauwe XML ophalen (hervatbaar)")
    sub.add_parser("index", help="fase 3: lokaal parsen naar SQLite")
    sub.add_parser("verify", help="controles op ruwe data en database")
    sub.add_parser("status", help="wat staat er al?")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    match args.command:
        case "plan":
            return asyncio.run(cmd_plan(args))
        case "harvest":
            return asyncio.run(cmd_harvest(args))
        case "index":
            return cmd_index(args)
        case "verify":
            return cmd_verify(args)
        case "status":
            return cmd_status(args)
    return 2


if __name__ == "__main__":
    sys.exit(main())
