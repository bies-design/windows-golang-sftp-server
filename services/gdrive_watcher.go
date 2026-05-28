package services

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"

	"intelligent-bim-data-conversion-hub/models"
	"intelligent-bim-data-conversion-hub/utilities"
)

type GDriveWatcher struct {
	driveService *drive.Service
	targetFolder string
	localTmpDir  string
	taskMgr      *models.Manager
}

func NewGDriveWatcher(ctx context.Context, credsPath, targetFolder, localTmpDir string, taskMgr *models.Manager) (*GDriveWatcher, error) {
	srv, err := drive.NewService(ctx, option.WithCredentialsFile(credsPath))
	if err != nil {
		return nil, err
	}
	return &GDriveWatcher{
		driveService: srv,
		targetFolder: targetFolder,
		localTmpDir:  localTmpDir,
		taskMgr:      taskMgr,
	}, nil
}

func (gw *GDriveWatcher) Start(ctx context.Context, interval time.Duration) {
	utilities.Info("🟢 [GDrive] 監控 Worker 已在背景啟動...")

	startToken, err := gw.driveService.Changes.GetStartPageToken().Do()
	if err != nil {
		utilities.Error("❌ [GDrive] 無法獲取起始 PageToken: %v", err)
		return
	}
	pageToken := startToken.StartPageToken

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			utilities.Info("🟥 [GDrive] 收到關閉訊號，監控 Worker 停止。")
			return
		case <-ticker.C:
			cl, err := gw.driveService.Changes.List(pageToken).
				Fields("nextPageToken, newStartPageToken, changes(fileId, removed, file(name, parents, mimeType))").
				Do()
			if err != nil {
				utilities.Warn("⚠️ [GDrive] 讀取變更清單失敗: %v", err)
				continue
			}

			for _, change := range cl.Changes {
				if change.Removed || change.File == nil {
					continue
				}

				// 1. 過濾是否屬於目標資料夾
				isTarget := false
				for _, parentID := range change.File.Parents {
					if parentID == gw.targetFolder {
						isTarget = true
						break
					}
				}

				// 2. 只有特定 BIM 擴充檔名（如 .3dm）才進行下載與轉檔觸發
				if isTarget && filepath.Ext(change.File.Name) == ".3dm" {
					utilities.Info("💡[GDrive] 偵測到新 BIM 檔案: %s", change.File.Name)

					// 移動到系統標準的 3dm 放檔區域，讓 Pipeline 能正確讀取
					final3dmPath := filepath.Join(filepath.Dir(gw.localTmpDir), "3dm", change.File.Name)

					// 執行檔案歷史備份 (VCS 插入)
					gw.taskMgr.BackupExistingFile(filepath.Dir(gw.localTmpDir), change.File.Name)

					// 下載
					if err := gw.downloadFile(change.FileId, final3dmPath); err != nil {
						utilities.Warn("⚠️ [GDrive] 下載 %s 失敗: %v", change.File.Name, err)
						continue
					}

					// 3. ✨ 呼叫你現有的 FSM Task 核心，拋入 Pipeline
					taskID := models.GenerateTaskID("gdrive_pipeline")
					gw.taskMgr.AddTask(&models.Task{
						ID:       taskID,
						TaskType: "pipeline",
						FilePath: change.File.Name,
						Status:   "processing",
					})

					// 異動非阻塞執行轉檔，不卡死 GDrive 監控
					go gw.taskMgr.StartPipeline(taskID, final3dmPath)
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