// Downloading the database from Zenodo.
//
// The file is large (≈1.5 GB), so the download goes to <dest>.part and is
// resumed with an HTTP Range request when that file already exists. Only
// after the md5 checksum from the Zenodo API matches is it renamed to dest.
package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// zenodoRecordURL is the API address of the published dataset. A variable so
// tests can point it at a local server.
var zenodoRecordURL = "https://zenodo.org/api/records/22798123"

var httpClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 60 * time.Second,
	},
}

// remoteFile is the database file as the Zenodo API describes it.
type remoteFile struct {
	URL  string
	Size int64
	MD5  string // hex
}

func fetchRemoteFile(ctx context.Context) (remoteFile, error) {
	var rec struct {
		Files []struct {
			Key      string `json:"key"`
			Size     int64  `json:"size"`
			Checksum string `json:"checksum"` // "md5:<hex>"
			Links    struct {
				Self string `json:"self"`
			} `json:"links"`
		} `json:"files"`
	}
	resp, err := get(ctx, zenodoRecordURL, 0)
	if err != nil {
		return remoteFile{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return remoteFile{}, fmt.Errorf("Zenodo antwoordt met %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		return remoteFile{}, fmt.Errorf("onverwacht antwoord van Zenodo: %w", err)
	}
	for _, f := range rec.Files {
		if f.Key != dbFileName {
			continue
		}
		sum, ok := strings.CutPrefix(f.Checksum, "md5:")
		if !ok || f.Links.Self == "" || f.Size <= 0 {
			return remoteFile{}, errors.New("onvolledige bestandsgegevens van Zenodo")
		}
		return remoteFile{URL: f.Links.Self, Size: f.Size, MD5: sum}, nil
	}
	return remoteFile{}, fmt.Errorf("%s staat niet (meer) in het Zenodo-record", dbFileName)
}

func get(ctx context.Context, url string, from int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sgdmeta-app")
	if from > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", from))
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errCancelled
		}
		return nil, fmt.Errorf("geen verbinding met Zenodo: %w", err)
	}
	return resp, nil
}

// progressFunc reports a stage ("downloaden", "controleren") and bytes done
// out of total.
type progressFunc func(stage string, done, total int64)

// downloadDB downloads the database to dest. Cancelling ctx keeps the .part
// file, so the next call continues where this one stopped.
func downloadDB(ctx context.Context, dest string, report progressFunc) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	report("verbinden", 0, 0)
	rf, err := fetchRemoteFile(ctx)
	if err != nil {
		return err
	}
	part := dest + ".part"
	if err := fetchPart(ctx, rf, part, report); err != nil {
		return err
	}

	report("controleren", 0, rf.Size)
	sum, err := md5File(ctx, part, func(n int64) { report("controleren", n, rf.Size) })
	if err != nil {
		return err
	}
	if !strings.EqualFold(sum, rf.MD5) {
		os.Remove(part)
		return errors.New("het gedownloade bestand is beschadigd (controlesom klopt niet); probeer het opnieuw")
	}
	return os.Rename(part, dest)
}

// fetchPart makes part a complete copy of rf, continuing what is there.
func fetchPart(ctx context.Context, rf remoteFile, part string, report progressFunc) error {
	var have int64
	if fi, err := os.Stat(part); err == nil {
		have = fi.Size()
	}
	if have > rf.Size { // left over from another version of the file
		have = 0
	}
	if have == rf.Size {
		return nil
	}

	resp, err := get(ctx, rf.URL, have)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0:
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		flags |= os.O_TRUNC // server ignored Range: start over
		have = 0
	default:
		return fmt.Errorf("download mislukt: Zenodo antwoordt met %s", resp.Status)
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	report("downloaden", have, rf.Size)
	done := have
	buf := make([]byte, 256<<10)
	last := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return fmt.Errorf("schrijven mislukt (is de schijf vol?): %w", err)
			}
			done += int64(n)
			if time.Since(last) > 200*time.Millisecond {
				report("downloaden", done, rf.Size)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return errCancelled
			}
			return fmt.Errorf("verbinding verbroken na %s; opnieuw starten gaat verder waar hij was: %w",
				humanBytes(done), rerr)
		}
	}
	report("downloaden", done, rf.Size)
	if err := f.Close(); err != nil {
		return err
	}
	if done != rf.Size {
		return fmt.Errorf("download onvolledig (%s van %s); probeer het opnieuw", humanBytes(done), humanBytes(rf.Size))
	}
	return nil
}

func md5File(ctx context.Context, path string, progress func(int64)) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	buf := make([]byte, 1<<20)
	var done int64
	for {
		if ctx.Err() != nil {
			return "", errCancelled
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		done += int64(n)
		if err == io.EOF {
			progress(done)
			break
		}
		if done%(64<<20) < int64(n) {
			progress(done)
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d kB", n>>10)
	}
}
