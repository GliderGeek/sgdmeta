"""Client voor de SRU-API van repository.overheid.nl.

Gemeten eigenschappen van de API:
  - maximumRecords maximum is 1000; 2000 levert een diagnostic op
  - diepe paginering werkt tot minstens startRecord=450000
  - resultCountPrecision is "estimate", dus tellingen zijn schattingen
"""

from __future__ import annotations

import asyncio
import xml.etree.ElementTree as ET
from dataclasses import dataclass

import httpx

ENDPOINT = "https://repository.overheid.nl/sru"
MAX_RECORDS = 1000
USER_AGENT = "sgdmeta/0.1 (parlementair-historisch onderzoek)"

NS = {
    "sru": "http://docs.oasis-open.org/ns/search-ws/sruResponse",
    "gzd": "http://standaarden.overheid.nl/sru",
    "dcterms": "http://purl.org/dc/terms/",
    "ow": "http://standaarden.overheid.nl/wetgeving/",
    "c": "http://standaarden.overheid.nl/collectie/",
    "diag": "http://docs.oasis-open.org/ns/search-ws/diagnostic",
    "scan": "http://docs.oasis-open.org/ns/search-ws/scan",
}


class SruError(RuntimeError):
    """De server gaf een diagnostic of bleef onbereikbaar."""


@dataclass(frozen=True, slots=True)
class ScanTerm:
    value: str
    count: int


class Sru:
    """
    Async client. Gebruik als context manager.

    De server rate-limit met HTTP 429. Standaard daarom ruim eronder:
    sequentieel, 1 request per 5 seconden.
    """

    def __init__(
        self,
        endpoint: str = ENDPOINT,
        *,
        concurrency: int = 1,
        min_interval: float = 5.0,
        retries: int = 6,
        timeout: float = 120.0,
    ) -> None:
        self.endpoint = endpoint
        self.retries = retries
        self.min_interval = min_interval
        self._sem = asyncio.Semaphore(concurrency)
        self._pace_lock = asyncio.Lock()
        self._next_at = 0.0
        self._client = httpx.AsyncClient(
            http2=True,
            timeout=timeout,
            headers={"User-Agent": USER_AGENT},
            follow_redirects=True,
        )

    async def _pace(self) -> None:
        async with self._pace_lock:
            now = asyncio.get_running_loop().time()
            wait = self._next_at - now
            if wait > 0:
                await asyncio.sleep(wait)
                now = asyncio.get_running_loop().time()
            self._next_at = now + self.min_interval

    def _slow_down(self, factor: float = 1.5, cap: float = 30.0) -> None:
        """Na een 429 permanent rustiger doorgaan, niet alleen even wachten."""
        self.min_interval = min(cap, max(self.min_interval, 0.1) * factor)

    async def __aenter__(self) -> Sru:
        return self

    async def __aexit__(self, *exc: object) -> None:
        await self._client.aclose()

    async def _get(self, params: dict[str, object]) -> bytes:
        last: Exception | None = None
        for attempt in range(self.retries):
            try:
                await self._pace()
                async with self._sem:
                    r = await self._client.get(self.endpoint, params=params)

                if r.status_code == 429:
                    # Retry-After volgen als die er is, anders zelf een ruime
                    # pauze nemen; daarna structureel trager doorgaan.
                    self._slow_down()
                    delay = float(r.headers.get("Retry-After") or 0) or min(
                        60.0, 5.0 * 2**attempt
                    )
                    await asyncio.sleep(delay)
                    last = SruError("HTTP 429")
                    continue

                # Andere 4xx lost zich niet op door te wachten.
                if 400 <= r.status_code < 500:
                    raise SruError(f"HTTP {r.status_code} voor {r.url}")

                r.raise_for_status()
                return r.content
            except SruError:
                raise
            except (httpx.HTTPError, OSError) as exc:
                last = exc
                await asyncio.sleep(min(60.0, 2**attempt))
        raise SruError(f"opgegeven na {self.retries} pogingen: {last}")

    async def search(self, query: str, start: int = 1, maximum: int = MAX_RECORDS) -> bytes:
        """Eén pagina rauwe XML. Retourneert bytes, ongeparsed.

        Bewust ongeparsed: fase 2 schrijft dit onaangeraakt naar schijf zodat
        de parser later kan veranderen zonder opnieuw te harvesten.
        """
        if maximum > MAX_RECORDS:
            raise ValueError(f"maximumRecords > {MAX_RECORDS} wordt door de API geweigerd")
        return await self._get(
            {
                "operation": "searchRetrieve",
                "version": "2.0",
                "query": query,
                "maximumRecords": maximum,
                "startRecord": start,
            }
        )

    async def count(self, query: str) -> int:
        """Aantal records voor een query. Let op: dit is een schatting."""
        return parse_count(await self.search(query, start=1, maximum=0))

    async def scan(self, clause: str, maximum_terms: int = 500) -> list[ScanTerm]:
        """Termfrequenties voor een index. Gratis manier om waarden te vinden."""
        raw = await self._get(
            {
                "operation": "scan",
                "version": "2.0",
                "scanClause": clause,
                "maximumTerms": maximum_terms,
            }
        )
        return parse_scan(raw)


# -- parsing --------------------------------------------------------------

def _check_diagnostics(root: ET.Element) -> None:
    msgs = [(e.text or "").strip() for e in root.iter(f"{{{NS['diag']}}}message")]
    if msgs:
        raise SruError("; ".join(msgs))


def parse_count(raw: bytes) -> int:
    root = ET.fromstring(raw)
    _check_diagnostics(root)
    n = root.find("sru:numberOfRecords", NS)
    if n is None or n.text is None:
        raise SruError("geen numberOfRecords in respons")
    return int(n.text)


def parse_scan(raw: bytes) -> list[ScanTerm]:
    root = ET.fromstring(raw)
    _check_diagnostics(root)
    out: list[ScanTerm] = []
    for term in root.iter(f"{{{NS['scan']}}}term"):
        value = term.find("scan:value", NS)
        count = term.find("scan:numberOfRecords", NS)
        if value is not None:
            out.append(ScanTerm(value.text or "", int(count.text) if count is not None else 0))
    return out


def iter_records(raw: bytes) -> list[ET.Element]:
    """De <sru:record>-elementen uit een pagina."""
    root = ET.fromstring(raw)
    _check_diagnostics(root)
    return root.findall(".//sru:record", NS)


def cql_quote(value: str) -> str:
    """Zet een waarde in een CQL-stringliteral."""
    escaped = value.replace("\\", "\\\\").replace('"', '\\"')
    return f'"{escaped}"'
