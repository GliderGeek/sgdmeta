# sgdmeta — app

Een programma om de sgdmeta-database zonder SQL te gebruiken: het opent in de
browser, downloadt de database van Zenodo als die er nog niet is, en laat je een
selectie maken, bekijken en exporteren naar Excel. Alles draait op je eigen
computer.

## Gebruiken

Download `sgdmeta-mac.zip` of `sgdmeta.exe` van de releases-pagina en start het.
De browser opent vanzelf; het programma stopt een paar minuten nadat je het
tabblad sluit.

- De database (±1,5 GB) wordt gedownload naar je Downloads-map
  (`sgdmeta.sqlite3`); je kunt ook
  een bestand kiezen dat je al hebt.
- Een onderbroken download gaat bij opnieuw starten verder waar hij was, en wordt
  pas gebruikt als de controlesom klopt.
- Instellingen (gekozen database, laatste selectie, exportmap) staan in
  `sgdmeta/settings.json` in de configuratiemap van je systeem.

## Ontwikkelen

Vereist Go. Geen cgo: alles cross-compileert.

```sh
go test ./...
go run . --db ../harvester/data/sgdmeta.sqlite3   # met een bestaande database
./build.sh                                        # dist/: macOS .app + Windows .exe
```

Opbouw:

| Bestand | Wat |
|---|---|
| `main.go` | start |
| `webui.go` | lokale webserver + JSON-API, één instantie, stopt als de pagina weg is |
| `download.go` | database van Zenodo halen (hervatbaar, md5-controle) |
| `db.go` | database openen (alleen-lezen) en schemaversie controleren |
| `query.go` | exportview (JSON uit de pagina) → SQL, alleen via whitelist |
| `export.go` | exportview naar .xlsx |
| `web/index.html` | de pagina (vanilla, ingebouwd met `go:embed`) |

Een nieuw filter toevoegen: veld in `filterFields` (en zo nodig een operator in
`operators`) in `query.go`, plus het invoerveld in `web/index.html`
(`currentView()`).

Release: een tag `app-v1.2.3` pushen bouwt en publiceert de binaries.
`icon.icns` opnieuw maken na het aanpassen van `icon.png`: `./make-icns.sh` (macOS).
