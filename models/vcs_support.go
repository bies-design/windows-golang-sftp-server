package models

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// VersionRecord 記錄單一版本的持久化描述結構
type VersionRecord struct {
	VersionID  string    `json:"version_id"`  // 版本識別碼 (例如: V-1716361234)
	BackupName string    `json:"backup_name"` // 存在 .versions 下的實體檔名
	Size       int64     `json:"size"`
	ModTime    time.Time `json:"mod_time"`    // 該版本的實體最後修改時間
	ArchivedAt string    `json:"archived_at"` // 被封存存檔的系統時間
}

// BackupExistingFile 在檔案被覆蓋前，自動建立快照並更新該檔案的版本日誌
func (tm *Manager) BackupExistingFile(baseDir, relPath string) {
	cleanPath := filepath.Clean(relPath)
	fullPath := filepath.Join(baseDir, cleanPath)

	// 1. 檢查檔案是否存在，如果不存在（全新上傳），直接退出不需備份
	fi, err := os.Stat(fullPath)
	if os.IsNotExist(err) {
		return
	} else if err != nil {
		return
	}

	// 2. 解析檔名與建立隱藏資料夾 .versions
	fileName := filepath.Base(fullPath)
	ext := filepath.Ext(fileName)
	pureName := fileName[:len(fileName)-len(ext)]
	vcsDir := filepath.Join(baseDir, ".versions")
	_ = os.MkdirAll(vcsDir, 0755)

	// 3. 產出這一個版本的唯一實體備份檔案
	timestamp := time.Now().UnixNano()
	versionID := fmt.Sprintf("V-%d", time.Now().Unix())
	backupFileName := fmt.Sprintf("%s_%d%s", pureName, timestamp, ext)
	backupFullPath := filepath.Join(vcsDir, backupFileName)

	// 4. 執行實體檔案複製 (Snapshot)
	srcFile, err := os.Open(fullPath)
	if err != nil {
		return
	}
	defer srcFile.Close()

	dstFile, err := os.Create(backupFullPath)
	if err != nil {
		return
	}
	defer dstFile.Close()

	if _, err = io.Copy(dstFile, srcFile); err != nil {
		return
	}

	// 5. 讀取或建立該檔案專屬的版本元數據紀錄 JSON
	vcsLogPath := filepath.Join(vcsDir, pureName+"_vcs.json")
	var records []VersionRecord

	if logData, err := os.ReadFile(vcsLogPath); err == nil {
		_ = json.Unmarshal(logData, &records)
	}

	// 6. 追加最新的一筆歷史紀錄
	newRecord := VersionRecord{
		VersionID:  versionID,
		BackupName: backupFileName,
		Size:       fi.Size(),
		ModTime:    fi.ModTime(),
		ArchivedAt: time.Now().Format("2006-01-02 15:04:05"),
	}
	records = append(records, newRecord)

	// 7. 重新寫回磁碟，保證重啟不丟失
	updatedData, err := json.MarshalIndent(records, "", "  ")
	if err == nil {
		_ = os.WriteFile(vcsLogPath, updatedData, 0644)
	}
}