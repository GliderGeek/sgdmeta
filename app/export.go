// Writing an export view to an Excel file.
package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

const (
	excelMaxRows = 1_048_576
	sheetName    = "Export"
)

const defaultOutName = "sgdmeta-export"

// outBase turns user input into a plain file name without extension.
func outBase(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if strings.HasSuffix(strings.ToLower(name), ".xlsx") {
		name = name[:len(name)-len(".xlsx")]
	}
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = defaultOutName
	}
	return name
}

// writeExport writes all rows of v to path. It writes to a temporary file
// next to it first, so a failed export never leaves half a file behind.
// progress is called now and then with the number of rows written.
func writeExport(db *sql.DB, v View, path string, total int64, progress func(int64)) error {
	if total+1 > excelMaxRows {
		return fmt.Errorf("deze export heeft %d rijen; Excel kan er maximaal %d aan. "+
			"Maak de selectie kleiner", total, excelMaxRows-1)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sgdmeta-export-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName) // no-op after a successful rename

	if err := writeXLSX(db, v, tmpName, progress); err != nil {
		return err
	}
	_ = os.Chmod(tmpName, 0o644) // CreateTemp makes it private
	if err := os.Rename(tmpName, path); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("kan %s niet overschrijven; is het bestand nog open in Excel?", filepath.Base(path))
		}
		return err
	}
	return nil
}

func reportEvery(progress func(int64)) func() {
	var n int64
	return func() {
		n++
		if progress != nil && n%5000 == 0 {
			progress(n)
		}
	}
}

func writeXLSX(db *sql.DB, v View, path string, progress func(int64)) error {
	f := excelize.NewFile()
	defer f.Close()
	if err := f.SetSheetName("Sheet1", sheetName); err != nil {
		return err
	}
	sw, err := f.NewStreamWriter(sheetName)
	if err != nil {
		return err
	}
	bold, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return err
	}
	for i, c := range columns {
		if err := sw.SetColWidth(i+1, i+1, c.width); err != nil {
			return err
		}
	}
	if err := sw.SetPanes(&excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
		return err
	}
	header := make([]any, len(columns))
	for i, c := range columns {
		header[i] = excelize.Cell{StyleID: bold, Value: c.Label}
	}
	if err := sw.SetRow("A1", header); err != nil {
		return err
	}

	row := 1
	tick := reportEvery(progress)
	cells := make([]any, len(columns))
	err = v.eachRow(db, 0, func(rec []string) error {
		row++
		for i, s := range rec {
			cells[i] = s
		}
		cell, _ := excelize.CoordinatesToCellName(1, row)
		tick()
		return sw.SetRow(cell, cells)
	})
	if err != nil {
		return err
	}
	if err := sw.Flush(); err != nil {
		return err
	}
	// Not SaveAs: that picks the format from the extension, and the
	// temporary file has none.
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := f.WriteTo(out); err != nil {
		return err
	}
	return out.Close()
}
