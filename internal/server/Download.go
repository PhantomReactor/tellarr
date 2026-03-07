package server

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
)

type Download struct {
	Filaname string
	Total    int64
	Written  atomic.Int64
	w        io.Writer
}

func (d *Download) WriteAt(b []byte, off int64) (int, error) {
	n, err := d.w.(*os.File).WriteAt(b, off)
	if err != nil {
		return n, err
	}
	d.Written.Add(int64(n))
	return n, err
}

func (d *Download) Percent() float64 {
	return (float64(d.Written.Load()) / float64(d.Total)) * 100
}

type DownloadManager struct {
	mu     sync.Mutex
	active map[string]*Download
}

func NewDownloadManger() DownloadManager {
	return DownloadManager{active: make(map[string]*Download)}
}

func (dm *DownloadManager) Add(id string, d *Download) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	dm.active[id] = d
}

func (dm *DownloadManager) Remove(id string) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	delete(dm.active, id)
}

func (dm *DownloadManager) Get(id string) *Download {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	return dm.active[id]
}

func (dm *DownloadManager) StartDownload(ctx context.Context, api *tg.Client, doc *tg.Document, filename string) (string, error) {
	downloadPath := os.Getenv("DOWNLOAD_DIR")
	if !strings.HasSuffix(downloadPath, "/") {
		downloadPath += "/"
	}
	path := filepath.Join(downloadPath, filename)
	file, err := os.Create(path)
	if err != nil {
		return "", err
	}

	dl := &Download{
		Filaname: filename,
		Total:    doc.Size,
		w:        file,
	}
	id := uuid.New().String()
	dm.Add(id, dl)

	go func() {
		defer dm.Remove(id)
		defer file.Close()
		d := downloader.NewDownloader()
		d.Download(api, &tg.InputDocumentFileLocation{
			ID:            doc.ID,
			AccessHash:    doc.AccessHash,
			FileReference: doc.FileReference,
		}).WithThreads(8).Parallel(ctx, dl)
	}()
	return id, nil
}
