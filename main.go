package main

import (
	"context"
	"fmt"			// 補上此行：用於第 145 行的 fmt.Fprintf
	"path/filepath" // 補上此行：用於第 114, 115, 138 行的 filepath 操作

	"os"
	"time"
	"embed"

	"os/signal"
	"syscall"

	flag "github.com/spf13/pflag"

	"github.com/spf13/viper"

	// 統一引入 models 套件
	"intelligent-bim-data-conversion-hub/models"
	"intelligent-bim-data-conversion-hub/services"
	"intelligent-bim-data-conversion-hub/utilities"
)
//go:embed index.html
var webAssets embed.FS

// ✨ 新增防護函式：具備超時機制的目錄初始化守衛
// 能有效防止網路磁碟機斷線時，程序永久卡死在 os.MkdirAll 的窘境
func initDirectoriesWithTimeout(dataDir string, timeout time.Duration) error {
	// 使用大小為 1 的緩衝通道，確保即使主執行緒因超時離開，背景 Goroutine 寫入時也不會阻塞卡死
	ch := make(chan error, 1)

	go func() {
		// 在背景協程中安全地執行磁碟 I/O 操作
		if err := os.MkdirAll(dataDir, 0755); err != nil {
			ch <- fmt.Errorf("無法建立根目錄: %v", err)
			return
		}
		if err := os.MkdirAll(filepath.Join(dataDir, "3dm"), 0755); err != nil {
			ch <- fmt.Errorf("無法建立 3dm 目錄: %v", err)
			return
		}
		if err := os.MkdirAll(filepath.Join(dataDir, "glb"), 0755); err != nil {
			ch <- fmt.Errorf("無法建立 glb 目錄: %v", err)
			return
		}
		if err := os.MkdirAll(filepath.Join(dataDir, "frag"), 0755); err != nil {
			ch <- fmt.Errorf("無法建立 frag 目錄: %v", err)
			return
		}
		if err := os.MkdirAll(filepath.Join(dataDir, ".versions"), 0755); err != nil {
			ch <- fmt.Errorf("無法建立 .versions 歷史庫: %v", err)
			return
		}
		ch <- nil // 全部成功建立
	}()

	// 進行時序調度監控
	select {
	case err := <-ch:
		return err // 在限時內完成，回傳實際結果 (成功或失敗)
	case <-time.After(timeout):
		// 超過指定時間背景仍無回應，判定為遠端磁碟連線異常
		return fmt.Errorf("存取網路磁碟超時 (%v)！遠端儲存伺服器可能處於離線、睡眠或尚未掛載準備就緒的狀態", timeout)
	}
}

func main() {
	// ==================== 0. 預備紀錄 ====================
	// 在正式初始化 Logger 之前，先將讀取設定檔的過程中可能產生的訊息暫存起來，等 Logger 就緒後再統一輸出
	type logRecord struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	}

	var beforeLogQueue = make([]logRecord, 0, 10) // 預留 10 個空間提升效能
	
	// 建立一個方便塞入緩衝的輔助匿名函式
	queueLog := func(level, format string, v ...interface{}) {
		msg := fmt.Sprintf(format, v...)
		beforeLogQueue = append(beforeLogQueue, logRecord{Level: level, Message: msg})
	}

	// ==================== 1. 初始化設定 ====================
	viper.SetDefault("SFTP_PORT", "2022")
	viper.SetDefault("API_PORT", "8088")
	viper.SetDefault("DATA_DIR", "./sftp_data")
	viper.SetDefault("MAX_UPLOADS", 5)
	viper.SetDefault("MAX_DOWNLOADS", 5)
	viper.SetDefault("LOG_LEVEL", "debug")
	viper.SetDefault("GDRIVE_INTERVAL", "15s")
	viper.SetDefault("GDRIVE_FOLDER_ID", "你的雲端硬碟BIM資料夾ID")

	// ✨ 指定 Viper 去讀取本地的 .env 檔案作為設定檔, 此處不用遵守 EnvPrefix 規範
	// viper.SetConfigFile(".env") // 不使用此功能，因為優先度太高沒有彈性
	viper.SetConfigName(".env")
	viper.SetConfigType("env")

	viper.AddConfigPath(".")       // 尋找路徑 1：執行當前根目錄
	viper.AddConfigPath("./config") // 尋找路徑 2：config 資料夾內

	if err := viper.ReadInConfig(); err != nil {
		// 開發期如果找不到 .env 先印出提示，不強制崩潰（因為生產環境可能直接走 Docker Env）
		currentDir, _ := os.Getwd()
		queueLog("warn", "[Debug] 目前程式工作目錄 (Working Dir): %s", currentDir)
		queueLog("warn", "[提示] 未找到 .env 設定檔，將完全採用預設值或作業系統環境變數。 %s, 原因:%+v", "error", err)
	} else {
		queueLog("info", "✅ 成功載入設定檔: %s", viper.ConfigFileUsed())
	}

	queueLog("info", "🚀 智能 BIM 數據轉換中心系統啟動中...") 
	queueLog("debug", "這條「會」被印出來！因為等級已經調低到 Debug 了")

	// 初始化日誌，預設為 Info 等級（隱藏 Debug）
	utilities.InitLogger((viper.GetString("LOG_LEVEL")), true, "") // 預設啟用自動換行，使用預設換行符號

	// 釋放(Flush)暫存的日誌
	for _, rec := range beforeLogQueue {
		switch rec.Level {
		case "debug":
			utilities.Debug(rec.Message)
		case "info":
			utilities.Info(rec.Message)
		case "warn":
			utilities.Warn(rec.Message)
		case "error":
			utilities.Error(rec.Message)
		default:
			utilities.Info(rec.Message)
		}
	}
	// 釋放完畢後清空切片，讓記憶體回收
	beforeLogQueue = nil

	// 限制全局的環境變數，只有 "INT_BIM_CH_" 前綴的才會被 Viper 自動讀取，避免不小心讀到其他無關的環境變數造成干擾
	viper.SetEnvPrefix("INT_BIM_CH_")
	// 允許從環境變數讀取設定，例如 SFTP_PORT, API_PORT, DATA_DIR 等
	viper.AutomaticEnv() 

	// 允許從命令列參數讀取設定，優先於環境變數
	flag.String("sftp-port", "", "SFTP 監聽的 Port")
	flag.String("data-dir", "", "檔案儲存的根目錄")
	flag.Parse()

	viper.BindPFlag("SFTP_PORT", flag.Lookup("sftp-port"))
	viper.BindPFlag("DATA_DIR", flag.Lookup("data-dir"))

	sftpPort := viper.GetString("SFTP_PORT")
	apiPort := viper.GetString("API_PORT")
	dataDir := viper.GetString("DATA_DIR")

    // 引入 5 秒看門狗超時機制，阻止啟動時無底限卡死
	utilities.Info("[系統] 正在驗證與初始化工作目錄: %s ...", dataDir)
	if err := initDirectoriesWithTimeout(dataDir, 5*time.Second); err != nil {
		utilities.Error("❌ [Working Dir] 啟動失敗 ── %v", err)
		return // 直接終止程序，避免後續因目錄問題引發更多錯誤
	} else {
		utilities.Info("🟢 [Working Dir] 啟用成功: %s", dataDir)
	}

	// ==================== 2. 核心廣播控制器（Context） ====================
	// ✨ 統一廣播控制器實例，讓各模組能夠共享主要FSM 處理狀態，收斂未來結構狀態管理的複雜度
	// 這裡的 ctx 除了提供給 GDriveWatch，也會在優雅停機時派上用場
	ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

	// ==================== 3. 初始化任務管理核心與基礎服務 ====================
	taskMgr := models.NewManager()
	
	// ==================== 4. 非同步啟動三大觸發源 (HTTP / SFTP / GDrive) ====================

	// A. 啟動 HTTP API 服務 (抽離細節)
	httpServer := services.NewHTTPServer(apiPort, dataDir, taskMgr, webAssets)
	go httpServer.Start(ctx)

	// B. 啟動 SFTP 服務 (抽離細節)
	sftpServer := services.NewSFTPServer(sftpPort, dataDir, taskMgr)
	go sftpServer.Start(ctx)

	// C. 啟動 Google Drive 監控 Worker (加入可能性)
	gdriveInterval, _ := time.ParseDuration(viper.GetString("GDRIVE_INTERVAL"))
	gdriveWatcher, err := services.NewGDriveWatcher(
		ctx,
		"config/gdrive-creds.json",
		viper.GetString("GDRIVE_FOLDER_ID"),
		filepath.Join(dataDir, "incoming_gdrive"),
		taskMgr,
	)
	if err != nil {
		utilities.Error("❌ [GDrive] 監控模組初始化失敗: %v", err)
	} else {
		go gdriveWatcher.Start(ctx, gdriveInterval)
	}

	// ==================== 5. 監聽系統信號，實現全局「優雅停機」 ====================
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	
	<-quit // 阻塞在此，直到收到 Ctrl+C 或 Windows 關閉信號
	utilities.Warn("⚠️ 接收到終止信號，正在優雅關閉所有子系統與釋放連線資源...")

	// 廣播取消信號：通知 Google Drive 監控與正在進行的非同步任務安全退出
	cancel()

	// 給予 HTTP 服務額外的超時緩衝，讓當前正在傳輸的 BIM 大檔案可以傳完
	httpServer.Stop(5 * time.Second)

	utilities.Info("🏁 數據轉換中心已安全關閉。")
}
