package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"			// 補上此行：用於第 145 行的 fmt.Fprintf
	"path/filepath" // 補上此行：用於第 114, 115, 138 行的 filepath 操作
	"io"
	// "log"
	"net"
	"net/http"
	"os"
	"time"
	"embed"

	flag "github.com/spf13/pflag"

	"github.com/pkg/sftp"
	"github.com/spf13/viper"
	"golang.org/x/crypto/ssh"

	// 統一引入 models 套件
	"intelligent-bim-data-conversion-hub/models"

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
		if err := os.MkdirAll(filepath.Join(dataDir, "toGlb"), 0755); err != nil {
			ch <- fmt.Errorf("無法建立 toGlb 目錄: %v", err)
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
	// ==================== 1. 初始化設定 ====================
	viper.SetDefault("SFTP_PORT", "2022")
	viper.SetDefault("API_PORT", "8088")
	viper.SetDefault("DATA_DIR", "./sftp_data")
	viper.SetDefault("MAX_UPLOADS", 5)
	viper.SetDefault("MAX_DOWNLOADS", 5)
	viper.SetDefault("LOG_LEVEL", "debug")
	
	// 初始化日誌，預設為 Info 等級（隱藏 Debug）
	utilities.InitLogger((viper.GetString("LOG_LEVEL")), true, "") // 預設啟用自動換行，使用預設換行符號
	
	utilities.Info("系統啟動中...") 
	utilities.Debug("這條「會」被印出來！因為等級已經調低到 Debug 了")

	// ✨ 指定 Viper 去讀取本地的 .env 檔案作為設定檔, 此處不用遵守 EnvPrefix 規範
	viper.SetConfigFile(".env")
	viper.SetConfigType("env")
	if err := viper.ReadInConfig(); err != nil {
		// 開發期如果找不到 .env 先印出提示，不強制崩潰（因為生產環境可能直接走 Docker Env）
		utilities.Info("[提示] 未找到 .env 設定檔，將完全採用預設值或作業系統環境變數", "error", err)
	}

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
	maxUploads := viper.GetInt("MAX_UPLOADS")
	maxDownloads := viper.GetInt("MAX_DOWNLOADS")

    // ✨ 核心修正：引入 5 秒看門狗超時機制，阻止啟動時無底限卡死
	utilities.Info("[系統] 正在驗證與初始化工作目錄: %s ...", dataDir)
	if err := initDirectoriesWithTimeout(dataDir, 5*time.Second); err != nil {
		utilities.Error("❌ 啟動失敗 ── %v", err)
	}

	// ==================== 2. 實例初始化 (調用 models) ====================
	taskMgr := models.NewManager()
	backend := models.NewCustomSFTPBackend(dataDir, taskMgr, maxUploads, maxDownloads)

	handlers := sftp.Handlers{
		FileGet:  backend,
		FilePut:  backend,
		FileCmd:  backend,
		FileList: backend,
	}

	// ==================== 3. 啟動 API 服務 ====================
	go func() {
		// 功能 1 & 2 的進度監控 API
		http.HandleFunc("/api/tasks", func(w http.ResponseWriter, r *http.Request) {
			tasks := taskMgr.GetAllTasks()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(tasks)
		})

		// 功能 1：顯示當前 working folder ，程序重啟時，網頁一呼叫此 API 就會自動從實體磁碟載入
		http.HandleFunc("/api/files", func(w http.ResponseWriter, r *http.Request) {
			files, err := os.ReadDir(dataDir)
			if err != nil {
				http.Error(w, "無法讀取工作目錄", http.StatusInternalServerError)
				return
			}
			type FileItem struct {
				Name string `json:"name"`
				Size int64  `json:"size"`
				ModTime time.Time `json:"mod_time"` // 修改時間欄位
			}
			var list []FileItem
			for _, f := range files {
				// ✨ 自動過濾：只加載根目錄檔案，自動排除子資料夾（toGlb與frag不會被錯當成普通檔案列出）
				if !f.IsDir() && f.Name()[0] != '.' { // 同時過濾掉隱藏檔案或資料夾（例如 .versions）
					info, err := f.Info()
					if err == nil {
						list = append(list, FileItem{
							Name: f.Name(), 
							Size: info.Size(), 
							ModTime: info.ModTime(), // 修改時間
						})
					}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(list)
		})

		// ✨ API：動態掃描並獲取已產出的 GLB 檔案清單
		http.HandleFunc("/api/files/glb", func(w http.ResponseWriter, r *http.Request) {
			files, err := os.ReadDir(filepath.Join(dataDir, "toGlb"))
			if err != nil { http.Error(w, "無法讀取 GLB 目錄", http.StatusInternalServerError); return }
			type FileItem struct { Name string `json:"name"`; Size int64 `json:"size"`; ModTime time.Time `json:"mod_time"` }
			var list []FileItem
			for _, f := range files {
				if !f.IsDir() && f.Name()[0] != '.' {
					info, err := f.Info()
					if err == nil { list = append(list, FileItem{Name: f.Name(), Size: info.Size(), ModTime: info.ModTime()}) }
				}
			}
			w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(list)
		})

		// ✨ API：動態掃描並獲取已產出的 Frag 檔案清單
		http.HandleFunc("/api/files/frag", func(w http.ResponseWriter, r *http.Request) {
			files, err := os.ReadDir(filepath.Join(dataDir, "frag"))
			if err != nil { http.Error(w, "無法讀取 Frag 目錄", http.StatusInternalServerError); return }
			type FileItem struct { Name string `json:"name"`; Size int64 `json:"size"`; ModTime time.Time `json:"mod_time"` }
			var list []FileItem
			for _, f := range files {
				if !f.IsDir() && f.Name()[0] != '.' {
					info, err := f.Info()
					if err == nil { list = append(list, FileItem{Name: f.Name(), Size: info.Size(), ModTime: info.ModTime()}) }
				}
			}
			w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(list)
		})

		// ✨ API：讀取 S3 持久化審計日誌
		http.HandleFunc("/api/s3-logs", func(w http.ResponseWriter, r *http.Request) {
			vcsLogPath := filepath.Join(dataDir, ".versions", "s3_upload_log.json")
			w.Header().Set("Content-Type", "application/json")
			if data, err := os.ReadFile(vcsLogPath); err == nil {
				w.Write(data)
			} else {
				w.Write([]byte("[]"))
			}
		})

		// 功能 3：基礎網頁端檔案上傳
		http.HandleFunc("/api/upload", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "僅支援 POST 請求", http.StatusMethodNotAllowed)
				return
			}
			// 限制上傳上限 150MB
			if err := r.ParseMultipartForm(150 << 20); err != nil {
				http.Error(w, "檔案太大", http.StatusBadRequest)
				return
			}

			file, header, err := r.FormFile("file")

			if err != nil {
				http.Error(w, "無效的檔案欄位", http.StatusBadRequest)
				return
			}
			defer file.Close()

			// 防禦 Directory Traversal 攻擊
			safeName := filepath.Base(header.Filename)
			fullPath := filepath.Join(dataDir, safeName)

			// ✨ 核心 VCS 插入點：網頁端覆蓋前同樣進行歷史備份
			taskMgr.BackupExistingFile(dataDir, safeName)

			out, err := os.Create(fullPath)
			if err != nil {
				http.Error(w, "伺服器無法建立檔案", http.StatusInternalServerError)
				return
			}
			defer out.Close()

			if _, err := io.Copy(out, file); err != nil {
				http.Error(w, "寫入檔案失敗", http.StatusInternalServerError)
				return
			}

			// 註冊任務並非同步拋入 Pipeline
			taskID := models.GenerateTaskID("pipeline")
			taskMgr.AddTask(&models.Task{
				ID:       taskID,
				TaskType: "pipeline",
				FilePath: safeName,
				Status:   "processing",
			})

			if filepath.Ext(safeName) == ".3dm" {
				go taskMgr.StartPipeline(taskID, fullPath)
			} else {
				taskMgr.UpdateStatus(taskID, "completed")
			}

			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"message":"網頁端上傳成功","task_id":"%s"}`, taskID)
		})

		// ✨ 功能 4：提供前端獲取特定檔案的版本演進歷史
		http.HandleFunc("/api/versions", func(w http.ResponseWriter, r *http.Request) {
			fileName := r.URL.Query().Get("file")
			if fileName == "" { http.Error(w, "缺少 file 參數", http.StatusBadRequest); return }
			
			ext := filepath.Ext(fileName)
			pureName := fileName[:len(fileName)-len(ext)]
			vcsLogPath := filepath.Join(dataDir, ".versions", pureName+"_vcs.json")
			
			w.Header().Set("Content-Type", "application/json")
			// 如果日誌檔存在就輸出，不存在就直接回傳空陣列
			if data, err := os.ReadFile(vcsLogPath); err == nil {
				w.Write(data)
			} else {
				w.Write([]byte("[]"))
			}
		})

		// 網頁首頁 UI 渲染
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")

			// 開發期動態讀取實體檔案 (改 HTML 免重啟 Go)
			if data, err := os.ReadFile("index.html"); err == nil {
				w.Write(data)
			} else {
				// 生產期安全降級：改由 webAssets 虛擬檔案系統中讀取 index.html 的 Byte 陣列
				if embedData, err := webAssets.ReadFile("index.html"); err == nil {
					w.Write(embedData)
				} else {
					http.Error(w, "BIM 控制台內嵌網頁加載失敗", http.StatusInternalServerError)
				}
			}
		})

		utilities.Info("🟢 [API] 伺服器已啟動，監聽 Port: %s", apiPort)
		if err := http.ListenAndServe(":"+apiPort, nil); err != nil {
			utilities.Error("❌ API http 伺服器啟動失敗: %v", err)
		}
		else {
			utilities.Warn("🔥 API 核心, http 服務啟動")
		}

	}()

	// ==================== 4. SSH 配置 ====================
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	signer, _ := ssh.NewSignerFromKey(key)
	// config := &ssh.ServerConfig{NoClientAuth: true}
	// 修正：廢棄 NoClientAuth，改用萬用密碼驗證回呼（PasswordCallback）
	// 這樣不論圖形化工具送出什麼帳號、密碼，伺服器都會正常回傳「通過」，避免工具無所適從
	config := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			utilities.Info("[Auth] 使用者 %s 正在嘗試登入", c.User())
			return nil, nil // 返回 nil 代表密碼驗證成功，允許登入
		},
	}
	config.AddHostKey(signer)

	// ==================== 5. 啟動 SFTP 服務 ====================
	listener, err := net.Listen("tcp", ":"+sftpPort) // 🔥 SFTP 核心
	if err != nil {
		utilities.Error("無法監聽 SFTP Port: %v", err)
	}
	utilities.Info("🟢 [SFTP] 伺服器已啟動, 監聽 Port: %s, 儲存目錄: %s", sftpPort, dataDir)

	for {
		nConn, err := listener.Accept()
		if err != nil {
			utilities.Info("接受 TCP 連線失敗: %v", err)
			continue
		}

		go func(conn net.Conn) {
			_, chans, reqs, err := ssh.NewServerConn(conn, config)
			if err != nil {
				utilities.Info("SSH 握手失敗: %v", err)
				return
			}
			go ssh.DiscardRequests(reqs)

			for newChannel := range chans {
				if newChannel.ChannelType() != "session" {
					newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
					continue
				}
				channel, requests, err := newChannel.Accept()
				if err != nil {
					continue
				}

				go func(in <-chan *ssh.Request) {
					for req := range in {
						ok := false
						switch req.Type {
						case "subsystem":
							if string(req.Payload[4:]) == "sftp" {
								ok = true
								server := sftp.NewRequestServer(channel, handlers)
								go func() {
									defer server.Close()
									if err := server.Serve(); err == io.EOF {
										server.Close()
									}
								}()
							}
						}
						req.Reply(ok, nil)
					}
				}(requests)
			}
		}(nConn)
	}
}

