// sgdmeta-app makes the Staten-Generaal Digitaal metadata database usable
// without SQL: it downloads the database from Zenodo when it is missing, lets
// the user compose an export view with filters, previews it and exports it to
// Excel or CSV.
//
// The user interface opens in the default browser (webui.go, web/index.html);
// everything runs on this machine.
//
//   - download.go: fetch sgdmeta.sqlite3 from Zenodo (resumable, md5-checked)
//   - db.go:       locate, open (read-only) and check the database
//   - query.go:    export view (JSON) → parameterised SQL, via a whitelist
//   - export.go:   write a view to .xlsx or .csv
//
// User-facing text is Dutch; code and comments are English.
package main

import (
	_ "embed"
	"errors"
	"flag"
)

//go:embed icon.png
var iconPNG []byte

const appName = "sgdmeta"

var errCancelled = errors.New("geannuleerd")

func main() {
	dbFlag := flag.String("db", "", "pad naar sgdmeta.sqlite3 (standaard: de eerder gekozen of gedownloade database)")
	flag.Parse()
	runGUI(*dbFlag)
}
