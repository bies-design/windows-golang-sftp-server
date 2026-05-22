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

// ✨ S3 設備上傳審計紀錄結構體
type S3Record struct {
	FileName  string `json:"file_name"`
	Timestamp string `json:"timestamp"`
	Status    string `json:"status"` // "成功" 或 "失敗: 原因"
}

// BackupExistingFile 在檔案被覆蓋前，自動建立快照並更新該檔案的版本日誌，限制最多三個歷史版本
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

	// ✨ 7. 核心容量守衛：強制限額 3 次。若超過則執行先進先出 (FIFO) 刪除
	// 使用 for 循環具備高度防禦性，能防止人工手動修改 JSON 導致多餘檔案殘留
	for len(records) > 3 {
		// 陣列最前端 [0] 即是最早的版本
		oldestRecord := records[0]
		oldestFullPath := filepath.Join(vcsDir, oldestRecord.BackupName)

		// 強制刪除實體舊 3dm 檔案，放任錯誤處理 (避免因檔案被手動砍掉時導致程式中斷)
		_ = os.Remove(oldestFullPath)

		// 核心切片移位：將最早的紀錄從 Go 陣列中剝離 (Pop)
		records = records[1:]
	}

	// 8. 重新寫回磁碟，保證重啟不丟失
	updatedData, err := json.MarshalIndent(records, "", "  ")
	if err == nil {
		_ = os.WriteFile(vcsLogPath, updatedData, 0644)
	}
}

// ✨ 將 S3 上傳稽核紀錄安全地以先進先出（FIFO）滾動寫入實體 JSON 檔案
// 外部網路 I/O 操作，極易因網路波動、憑證過期或儲存空間不足而失敗，因此不應該讓任何錯誤影響到核心轉換流程
func (tm *Manager) LogS3Upload(baseDir string, fileName string, success bool, errMsg string) {
	vcsDir := filepath.Join(baseDir, ".versions")
	_ = os.MkdirAll(vcsDir, 0755)
	logPath := filepath.Join(vcsDir, "s3_upload_log.json")

	var records []S3Record
	if logData, err := os.ReadFile(logPath); err == nil {
		_ = json.Unmarshal(logData, &records)
	}

	statusStr := "成功"
	if !success {
		statusStr = fmt.Sprintf("失敗: %s", errMsg)
	}

	newLog := S3Record{
		FileName:  fileName,
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		Status:    statusStr,
	}
	records = append(records, newLog)

	// 確保 S3 傳輸稽核日誌最大保留 100 筆，避免撐爆磁碟
	if len(records) > 100 {
		records = records[len(records)-100:]
	}

	updatedData, err := json.MarshalIndent(records, "", "  ")
	if err == nil {
		_ = os.WriteFile(logPath, updatedData, 0644)
	}
}