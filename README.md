# windows-golang-Intelligent-BIM-Data-Conversion-Hub
邊緣運行Rhino Compute服務的組件之一 <br/>
管理核心：智慧型 BIM 數據轉換樞紐

## ENV
windows 11 Home, 64 位元 <br/>
g [github link](https://github.com/voidint/g "版本切換器")  <br/>
```bash
$ ~/.g/bin/g.exe -v
g version 1.8.0
Built:         2025-07-11 13:24:15
Git branch:    master
Git commit:    a82e89cc
Go version:    go1.20.14
OS/Arch:       windows/amd64
Experimental:  false
```
Golang 
```bash
go1.25.10 windows/amd64 
```

### Filesystem and explain
```text
├── main.go                     # 核心骨架，只負責初始化、設定讀取與生命週期調度
├── config/                     # 放 .env 或 gdrive-creds.json
├── models/                     # FSM, TaskManager, CustomSFTPBackend, googleDriveWatcher
├── utilities/                  # Logger 與 副檔名是否支援檢查工具(frag 產出確認)
├── methods/                    # 轉檔流程實作方案
└── services/                   # 存放抽離出來的伺服器與監控實作
    ├── http_server.go          # HTTP API 路由與設定細節
    ├── sftp_server.go          # SFTP SSH 握手與請求服務細節
    └── gdrive_watcher.go       # Google Drive 監控與 Pipeline 觸發

```

### Tools
1. PowerShell ENV

terminate
```bash
... > code $PROFILE
# this command will open the default PowerShell configuration file using VSCode.
```
VsCode
```toml
$env:GOROOT="$HOME\.g\go"
$env:Path=-join("$HOME\.g\bin;", "$env:GOROOT\bin;", "$env:Path")
```

2. Git for Windows: Bash ENV

terminate
```bash
... > code ~/.bash_profile
# this command will open the default bash configuration file using VSCode.
```
VsCode
```toml
GOROOT="$HOME\.g\go"
PATH="$HOME\.g\bin:$GOROOT\bin:$PATH"
```

## Setup

### dependent
```bash
go mod init intelligent-bim-data-conversion-hub
go get github.com/pkg/sftp
go get golang.org/x/crypto/ssh
# ------------------aws s3 interface---------------------
go get github.com/aws/aws-sdk-go-v2
go get github.com/aws/aws-sdk-go-v2/config
go get github.com/aws/aws-sdk-go-v2/credentials
go get github.com/aws/aws-sdk-go-v2/service/s3
go get github.com/aws/aws-sdk-go-v2/feature/s3/manager
# ------------------ ^^^^^^^^^^^^^^ ---------------------
go get github.com/spf13/viper
# ------------------ google drive --------------------
go get google.golang.org/api/drive/v3
go get google.golang.org/api/option
# ------------------ ^^^^^^^^^^^^ ----------------------
go mod tidy

```

### start
fast start up <br/>
```bash
$ go run main.go --sftp-port=3022 --data-dir=/d/readyToConvert
----
2026/05/20 18:00:26 [API] 伺服器已啟動，監聽 Port: 8088
2026/05/20 18:00:26 [SFTP] 伺服器已啟動，監聽 Port: 3022, 儲存目錄: D:/readyToConvert
```
研發時期 <br/>
```bash
go test -v -run TestRunServer -timeout 0
```
正式機組 <br/>
```bash
go build -o go_bim_data_conversion_hub.exe main.go
./go_bim_data_conversion_hub.exe
```

### build

## P.S.

> 補充說明 <br/>

本系統旨在不依賴任何外部資料庫（如 MySQL, PostgreSQL）的前提下，於本地檔案系統中為重型 3D 建築模型資產（如 `.3dm`）建立 100% 持久化、重啟免維護的多版本控制體系。

### ⚙️ 設計理念 (Design Philosophy)

1. **零外部依賴 (Zero-Database Dependency)**
   BIM 模型檔案巨大，用資料庫儲存二進位或僅記錄狀態會使架構變得沉重。本系統將「版本元數據(JSON)」與「備份工件(Artifacts)」直接沉澱在硬碟。即便服務器重啟千百次，隨時掃描磁碟即可完成全量歷史重建。
2. **事前快照攔截 (Pre-truncation Snapshot)**
   不論是 Web 快速上傳還是標準的 SFTP 終端上傳，在底層呼叫 `os.O_TRUNC`（清空並覆蓋檔案）的前一刻，VCS 會強制插隊對舊檔案進行複製封存，確保歷史設計鏈條絕對不中斷。
3. **隔離性防禦目錄學 (Defensive Architecture)**
   所有歷史版本檔案和日誌全部收納在工作資料夾底下的隱藏目錄 `.versions/` 中。這對普通 SFTP 目錄瀏覽完全隱形，大幅降低設計師誤刪歷史版本的風險。

### 📖 使用與交互指南 (Usage Guide)

#### 後端觸發點 (Hooks)
* **網頁端上傳** (`main.go` -> `/api/upload`)：
  在 `os.Create(fullPath)` 前自動執行 `taskMgr.BackupExistingFile(dataDir, safeName)`。
* **SFTP 客戶端上傳** (`models/imp_sftp.go` -> `Filewrite`)：
  在 `os.OpenFile(..., os.O_TRUNC, ...)` 前自動執行 `b.TaskMgr.BackupExistingFile(b.BaseDir, cleanPath)`。
* **從Google Drive 上傳** (`models/watch_google_drive.go` -> `online folder update`)

#### 前端 UI 渲染流程
1. **工作目錄渲染**：網頁每 4 秒呼叫一次 `/api/files`，動態列出實體工作資料夾的當前活躍檔案，並過濾掉隱藏檔與 `.versions`。
2. **獲取版本歷史**：當用戶點擊某一檔案旁的 `📜 歷史版本` 按鈕時，前端發送請求至 `/api/versions?file=檔案名稱`。
3. **數據時間線對齊**：後端即時讀取對應的 `{純檔名}_vcs.json` 回傳。前端收到後進行**時間降序排列**，將最新被覆蓋的版本置頂，完美展示模型的進化歷程。

### 額外紀錄
```bash
main.go

//go:embed index.html
var htmlTemplate string
// ✨ 核心重構：利用編譯期內嵌指令，將同目錄下的 index.html 內容自動綁定到 htmlTemplate 變數
// 內嵌前端 UI 模板 
// (整合 Bootstrap 5 進行資料輪詢更新)
// (整合 localStorage 轉換歷史持久化紀錄，絕無反引號衝突)
// ⚠️ 注意：//go:embed 與底下的變數宣告之間「不能有空行」
```