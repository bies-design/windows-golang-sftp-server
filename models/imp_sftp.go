package models

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pkg/sftp"
)

// CustomSFTPBackend 實作 sftp.Handlers 介面
type CustomSFTPBackend struct {
	BaseDir     string
	TaskMgr     *Manager // 同 package，直接調用
	UploadSem   chan struct{}
	DownloadSem chan struct{}
}

// NewCustomSFTPBackend 初始化並回傳 SFTP 後端實作
func NewCustomSFTPBackend(baseDir string, taskMgr *Manager, maxUploads, maxDownloads int) *CustomSFTPBackend {
	return &CustomSFTPBackend{
		BaseDir:     baseDir,
		TaskMgr:     taskMgr,
		UploadSem:   make(chan struct{}, maxUploads),
		DownloadSem: make(chan struct{}, maxDownloads),
	}
}

// Filewrite 攔截寫入操作 (上傳)
func (b *CustomSFTPBackend) Filewrite(req *sftp.Request) (io.WriterAt, error) {
	select {
	case b.UploadSem <- struct{}{}:
	default:
		return nil, fmt.Errorf("達到最大上傳任務限制")
	}

	cleanPath := filepath.Clean(req.Filepath)
	fullPath := filepath.Join(b.BaseDir, cleanPath)
	
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		<-b.UploadSem
		return nil, err
	}

	file, err := os.OpenFile(fullPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		<-b.UploadSem
		return nil, err
	}

	taskID := GenerateTaskID("upload")
	b.TaskMgr.AddTask(&Task{ // 同 package，直接調用
		ID:       taskID,
		TaskType: "upload",
		FilePath: cleanPath,
		Status:   "running",
	})

	return &trackingWriter{
		file:    file,
		taskID:  taskID,
		taskMgr: b.TaskMgr,
		sem:     b.UploadSem,
	}, nil
}

// Fileread 攔截讀取操作 (下載)
func (b *CustomSFTPBackend) Fileread(req *sftp.Request) (io.ReaderAt, error) {
	select {
	case b.DownloadSem <- struct{}{}:
	default:
		return nil, fmt.Errorf("達到最大下載任務限制")
	}

	cleanPath := filepath.Clean(req.Filepath)
	fullPath := filepath.Join(b.BaseDir, cleanPath)

	file, err := os.Open(fullPath)
	if err != nil {
		<-b.DownloadSem
		return nil, err
	}

	taskID := GenerateTaskID("download")
	b.TaskMgr.AddTask(&Task{ // 同 package，直接調用
		ID:       taskID,
		TaskType: "download",
		FilePath: cleanPath,
		Status:   "running",
	})

	return &trackingReader{
		file:    file,
		taskID:  taskID,
		taskMgr: b.TaskMgr,
		sem:     b.DownloadSem,
	}, nil
}

// Filecmd 處理圖形化工具常見的系統指令 (建立目錄、刪除、改名、更新狀態)
func (b *CustomSFTPBackend) Filecmd(req *sftp.Request) error {
	cleanPath := filepath.Clean(req.Filepath)
	fullPath := filepath.Join(b.BaseDir, cleanPath)

	switch req.Method {
	case "Setstat":
		// GUI 客戶端上傳檔案後，經常會發送 Setstat 來修改檔案時間或權限
		// 直接回傳 nil 讓其順利通關
		return nil
	case "Mkdir":
		return os.MkdirAll(fullPath, 0755)
	case "Rmdir", "Remove":
		return os.RemoveAll(fullPath)
	case "Rename":
		targetPath := filepath.Join(b.BaseDir, filepath.Clean(req.Target))
		return os.Rename(fullPath, targetPath)
	}
	return sftp.ErrSSHFxOpUnsupported
}

// listerAt 實作 sftp.ListerAt 介面，供圖形化工具分段讀取目錄快照
type listerAt []os.FileInfo

func (l listerAt) ListAt(ls []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(ls, l[offset:])
	if n < len(ls) {
		return n, io.EOF
	}
	return n, nil
}

// Filelist 負責響應圖形化工具的目錄瀏覽 (List) 與檔案屬性查詢 (Stat)
func (b *CustomSFTPBackend) Filelist(req *sftp.Request) (sftp.ListerAt, error) {
	cleanPath := filepath.Clean(req.Filepath)
	fullPath := filepath.Join(b.BaseDir, cleanPath)

	switch req.Method {
	case "List":
		files, err := os.ReadDir(fullPath)
		if err != nil {
			return nil, err
		}
		var list []os.FileInfo
		for _, f := range files {
			info, err := f.Info()
			if err == nil {
				list = append(list, info)
			}
		}
		return listerAt(list), nil

	case "Stat", "Lstat":
		fi, err := os.Stat(fullPath)
		if err != nil {
			return nil, err
		}
		return listerAt([]os.FileInfo{fi}), nil
	}

	return nil, sftp.ErrSSHFxOpUnsupported
}

// trackingWriter 包裝 os.File 以追蹤寫入進度
type trackingWriter struct {
	file    *os.File
	taskID  string
	taskMgr *Manager
	sem     chan struct{}
	closed  bool
}

func (t *trackingWriter) WriteAt(p []byte, off int64) (int, error) {
	n, err := t.file.WriteAt(p, off)
	t.taskMgr.UpdateProgress(t.taskID, int64(n))
	return n, err
}

func (t *trackingWriter) Close() error {
	if !t.closed {
		t.taskMgr.UpdateStatus(t.taskID, "completed")
		<-t.sem
		t.closed = true
	}
	return t.file.Close()
}

// trackingReader 包裝 os.File 以追蹤讀取進度
type trackingReader struct {
	file    *os.File
	taskID  string
	taskMgr *Manager
	sem     chan struct{}
	closed  bool
}

func (t *trackingReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := t.file.ReadAt(p, off)
	t.taskMgr.UpdateProgress(t.taskID, int64(n))
	return n, err
}

func (t *trackingReader) Close() error {
	if !t.closed {
		t.taskMgr.UpdateStatus(t.taskID, "completed")
		<-t.sem
		t.closed = true
	}
	return t.file.Close()
}