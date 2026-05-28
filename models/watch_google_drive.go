package models

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"

	"intelligent-bim-data-conversion-hub/utilities"
)

// GDriveWatcher 負責監控雲端硬碟並觸發核心管線
type GDriveWatcher struct {
	driveService *drive.Service
	targetFolder string
	localTmpDir  string
	// 這裡接入你現有的系統核心處理函式，例如：
	// processPipeline func(localFilePath string) error
	TaskMgr     *Manager // 同 package，直接調用
}

func NewGDriveWatcher(ctx context.Context, credsPath, targetFolder, localTmpDir string, taskMgr *Manager) (*GDriveWatcher, error) {
	srv, err := drive.NewService(ctx, option.WithCredentialsFile(credsPath))
	if err != nil {
		return nil, err
	}
	
	// 確保本地暫存目錄存在
	if err := os.MkdirAll(localTmpDir, 0755); err != nil {
		return nil, err
	}

	return &GDriveWatcher{
		driveService: srv,
		targetFolder: targetFolder,
		localTmpDir:  localTmpDir,
		TaskMgr:      taskMgr,
	}, nil
}

// Start 開始背景監控迴圈
func (gw *GDriveWatcher) Start(ctx context.Context, interval time.Duration) {
	utilities.Info("[GDrive Watcher] 模組已啟動，開始監控 Google Drive 變更...")

	startToken, err := gw.driveService.Changes.GetStartPageToken().Do()
	if err != nil {
		utilities.Error("[GDrive Watcher] 無法獲取起始 PageToken: %v", err)
	}
	pageToken := startToken.StartPageToken

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			utilities.Info("[GDrive Watcher] 監控停止。")
			return
		case <-ticker.C:
			cl, err := gw.driveService.Changes.List(pageToken).
				Fields("nextPageToken, newStartPageToken, changes(fileId, removed, file(name, parents, mimeType))").
				Do()
			if err != nil {
				utilities.Warn("[GDrive Watcher] 讀取變更失敗: %v\n", err)
				continue
			}

			for _, change := range cl.Changes {
				if change.Removed || change.File == nil {
					continue
				}

				// 1. 檢查是否屬於目標資料夾
				isTarget := false
				for _, parentID := range change.File.Parents {
					if parentID == gw.targetFolder {
						isTarget = true
						break
					}
				}

				if isTarget {
					utilities.Info("[GDrive Watcher] 偵測到新檔案: %s\n", change.File.Name)
					
					// 2. 下載到本地暫存區
					localPath := filepath.Join(gw.localTmpDir, change.File.Name)
					err := gw.downloadFile(change.FileId, localPath)
					if err != nil {
						utilities.Warn("[GDrive Watcher] 檔案 %s 下載失敗: %v\n", change.File.Name, err)
						continue
					}

					// 3. ✨ 觸發你現有的 BIM 轉檔與 SeaweedFS 上傳管線
					utilities.Info("[GDrive Watcher] 成功下載，開始送入 BIM 轉檔核心: %s\n", localPath)
					
					// go yourExistingPipeline.Process(localPath) 
					// (建議用 goroutine非阻塞 處理，讓監控能繼續保持更新)
					gw.TaskMgr.UpdateStatus(GenerateTaskID("pipeline"), "processing")
					go gw.TaskMgr.StartPipeline(GenerateTaskID("pipeline"), localPath)
				} else {
					gw.TaskMgr.UpdateStatus(GenerateTaskID("pipeline"), "completed")
				}
			}

			if cl.NewStartPageToken != "" {
				pageToken = cl.NewStartPageToken
			}
		}
	}
}

func (gw *GDriveWatcher) downloadFile(fileID, destPath string) error {
	res, err := gw.driveService.Files.Get(fileID).Download()
	if err != nil {
		return err
	}
	defer res.Body.Close()

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, res.Body)
	return err
}