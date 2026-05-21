package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"			// 補上此行：用於第 145 行的 fmt.Fprintf
	"path/filepath" // 補上此行：用於第 114, 115, 138 行的 filepath 操作
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/pkg/sftp"
	"github.com/spf13/viper"
	"golang.org/x/crypto/ssh"

	// 統一引入 models 套件
	"sftp-server/models"
)

func main() {
	// ==================== 1. 初始化設定 ====================
	viper.SetDefault("SFTP_PORT", "2022")
	viper.SetDefault("API_PORT", "8088")
	viper.SetDefault("DATA_DIR", "./sftp_data")
	viper.SetDefault("MAX_UPLOADS", 5)
	viper.SetDefault("MAX_DOWNLOADS", 5)

	viper.SetEnvPrefix("SFTP_")
	viper.AutomaticEnv()

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

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("無法建立資料目錄: %v", err)
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

		// 功能 1：顯示當前 working folder 內已經完成上傳的檔案列表
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
				if !f.IsDir() {
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

		// 網頁首頁 UI 渲染
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(htmlTemplate))
		})

		log.Printf("[API] 伺服器已啟動，監聽 Port: %s", apiPort)
		log.Fatal(http.ListenAndServe(":"+apiPort, nil))
	}()

	// ==================== 4. SSH 配置 ====================
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	signer, _ := ssh.NewSignerFromKey(key)
	// config := &ssh.ServerConfig{NoClientAuth: true}
	// 修正：廢棄 NoClientAuth，改用萬用密碼驗證回呼（PasswordCallback）
	// 這樣不論圖形化工具送出什麼帳號、密碼，伺服器都會正常回傳「通過」，避免工具無所適從
	config := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			log.Printf("[Auth] 使用者 %s 正在嘗試登入", c.User())
			return nil, nil // 返回 nil 代表密碼驗證成功，允許登入
		},
	}
	config.AddHostKey(signer)

	// ==================== 5. 啟動 SFTP 服務 ====================
	listener, err := net.Listen("tcp", ":"+sftpPort)
	if err != nil {
		log.Fatalf("無法監聽 SFTP Port: %v", err)
	}
	log.Printf("[SFTP] 伺服器已啟動，監聽 Port: %s, 儲存目錄: %s", sftpPort, dataDir)

	for {
		nConn, err := listener.Accept()
		if err != nil {
			log.Printf("接受 TCP 連線失敗: %v", err)
			continue
		}

		go func(conn net.Conn) {
			_, chans, reqs, err := ssh.NewServerConn(conn, config)
			if err != nil {
				log.Printf("SSH 握手失敗: %v", err)
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

// 內嵌前端 UI 模板 
// (整合 Bootstrap 5 進行資料輪詢更新)
// (整合 localStorage 轉換歷史持久化紀錄，絕無反引號衝突)
const htmlTemplate = `
<!DOCTYPE html>
<html lang="zh-TW">
<head>
    <meta charset="UTF-8">
    <title>BIES Auto Converter SFTP 雲端管控工作台</title>
    <link href="https://cdn.jsdelivr.net/npm/bootstrap@5.3.0/dist/css/bootstrap.min.css" rel="stylesheet">
    <style>body { padding: 30px; background-color: #f4f6f9; } .card { margin-bottom: 25px; }</style>
</head>
<body>
    <div class="container">
        <h3 class="mb-4 text-secondary">⚙️ BIES Auto Converter SFTP 雲端控制台</h3>
        
        <div class="card shadow-sm">
            <div class="card-header bg-primary text-white font-weight-bold">📁 網頁端基本快速上傳 (.3dm)</div>
            <div class="card-body">
                <div class="input-group">
                    <input type="file" class="form-control" id="fileSelector">
                    <button class="btn btn-primary" type="button" onclick="executeUpload()">開始上傳檔案</button>
                </div>
                <div id="uploadReport" class="mt-2 small text-muted"></div>
            </div>
        </div>

        <div class="card shadow-sm">
            <div class="card-header bg-dark text-white font-weight-bold">⚡ 即時轉換與上傳流水線進度 (Rhino / ThatOpen / SeaweedFS)</div>
            <div class="card-body" id="pipelineBox">
                <span class="text-muted">目前系統無活躍中轉換任務...</span>
            </div>
        </div>

        <div class="card shadow-sm">
            <div class="card-header bg-success text-white font-weight-bold">📦 Working Folder 已完成上傳檔案清單</div>
            <div class="card-body">
                <table class="table table-striped table-hover align-middle">
                    <thead class="table-light"><tr><th>檔案名稱</th><th>檔案大小 (Bytes)</th></tr></thead>
                    <tbody id="fileTableBody"><tr><td colspan="2" class="text-center text-muted">讀取中...</td></tr></tbody>
                </table>
            </div>
        </div>
    </div>

    <script>// 初始化本地儲存空間鍵值
        const STORAGE_KEY = 'bim_sftp_history_logs';

        // 從 localStorage 讀取紀錄
        function getLocalHistory() {
            try {
                const data = localStorage.getItem(STORAGE_KEY);
                return data ? JSON.parse(data) : {};
            } catch (e) {
                return {};
            }
        }

        // 儲存紀錄至 localStorage
        function saveLocalHistory(historyMap) {
            localStorage.setItem(STORAGE_KEY, JSON.stringify(historyMap));
        }

        // 清除歷史紀錄
        function clearLocalHistory() {
            if (confirm('確定要清空瀏覽器本地的所有轉換歷史紀錄嗎？')) {
                localStorage.removeItem(STORAGE_KEY);
                renderHistoryUI();
            }
        }

        // 將當前的任務快照同步至歷史紀錄中
        function syncTasksToHistory(tasks) {
            if (!tasks || tasks.length === 0) return false;
            let historyMap = getLocalHistory();
            let hasChanged = false;

            tasks.forEach(function(t) {
                // 當任務狀態為已完成(completed)或失敗(failed)時，寫入/更新至歷史存檔
                if (t.status === 'completed' || t.status === 'failed') {
                    // 若此任務尚未被存檔，或者狀態有更新，則寫入
                    if (!historyMap[t.id] || historyMap[t.id].status !== t.status || historyMap[t.id].stage !== t.stage) {
                        historyMap[t.id] = {
                            id: t.id,
                            file_path: t.file_path,
                            type: t.type,
                            status: t.status,
                            stage: t.stage,
                            percent: t.percent,
                            error_msg: t.error_msg,
                            saved_at: new Date().toLocaleString()
                        };
                        hasChanged = true;
                    }
                }
            });

            if (hasChanged) {
                saveLocalHistory(historyMap);
                return true;
            }
            return false;
        }

        // 渲染歷史紀錄 UI, 加上防禦機制
        function renderHistoryUI() {
            const historyMap = getLocalHistory();
            const box = document.getElementById('historyListBox');

			// 🔥 關鍵修正：如果找不到元素，優雅退出，不讓程式崩潰
			if (!box) {
				console.warn('找不到 id 为 historyListBox 的元素，請檢查 HTML 結構。');
				return;
			}

            const keys = Object.keys(historyMap);

            if (keys.length === 0) {
                box.innerHTML = '<span class="text-muted">暫無歷史存檔紀錄</span>';
                return;
            }

            // 依時間降序排列 (新任務在前)
            keys.sort((a, b) => b.localeCompare(a));

            box.innerHTML = keys.map(function(key) {
                const t = historyMap[key];
                let color = t.status === 'completed' ? 'bg-success' : 'bg-danger';
                
                return '<div class="mb-2 p-2 border-bottom d-flex justify-content-between align-items-center">' +
                    '<div>' +
                        '<strong>📁 ' + t.file_path + '</strong> ' +
                        '<span class="badge bg-secondary font-monospace">' + t.type.toUpperCase() + '</span>' +
                        '<br><small class="text-muted">最後狀態: ' + (t.stage || '已結束') + '</small>' +
                        (t.error_msg ? '<span class="text-danger small ms-2">⚠️ ' + t.error_msg + '</span>' : '') +
                    '</div>' +
                    '<div class="text-end">' +
                        '<span class="badge ' + color + '">' + t.status.toUpperCase() + '</span>' +
                        '<div class="history-time mt-1">🕒 ' + t.saved_at + '</div>' +
                    '</div>' +
                '</div>';
            }).join('');
        }

        async function executeUpload() {
            const selector = document.getElementById('fileSelector');
            const report = document.getElementById('uploadReport');
            if (!selector.files.length) { alert('請選取要上傳的檔案'); return; }

            const fd = new FormData();
            fd.append('file', selector.files[0]);
            report.className = "mt-2 small text-warning";
            report.innerText = "檔案傳輸中...";

            try {
                const res = await fetch('/api/upload', { method: 'POST', body: fd });
                const data = await res.json();
                if (res.ok) {
                    report.className = "mt-2 small text-success";
                    report.innerText = data.message + " (TaskID: " + data.task_id + ")";
                    selector.value = "";
                    refreshFiles();
                } else { report.className = "mt-2 small text-danger"; report.innerText = "失敗: " + data.error; }
            } catch(e) { report.className = "mt-2 small text-danger"; report.innerText = "網路連線異常"; }
        }

		// 增強修正：依時間排序，最新一筆 10 分鐘內自動加上 ➕ 小圖示，提示使用者這是剛剛上傳的檔案
        async function refreshFiles() {
            try {
                const res = await fetch('/api/files');
                const list = await res.json();
                const tbody = document.getElementById('fileTableBody');
                if (!list || list.length === 0) { tbody.innerHTML = '<tr><td colspan="2" class="text-center text-muted">目前工作資料夾無實體檔案</td></tr>'; return; }

				// 🛠️ 修正：依據最後修改時間從新到舊排序 (Newest first)
                list.sort(function(a, b) {
                    return new Date(b.mod_time) - new Date(a.mod_time);
                });

                const now = new Date();
                const tenMinutesInMs = 10 * 60 * 1000;

                tbody.innerHTML = list.map(function(f, index) {
                    let prefix = '';
                    
                    // 🛠️ index === 0 代表這是最新加入/更改的檔案紀錄
                    if (index === 0) {
                        const fileTime = new Date(f.mod_time);
                        // 判定是否在 10 分鐘（600,000 毫秒）之內
                        if (now - fileTime <= tenMinutesInMs) {
                            prefix = '<span class="badge bg-success me-2">➕ 最新上傳</span> ';
                        }
                    }

                    const displayTime = new Date(f.mod_time).toLocaleString();

                    return '<tr>' +
                        '<td>' + prefix + '<strong>' + f.name + '</strong></td>' +
                        '<td>' + f.size.toLocaleString() + '</td>' +
                        '<td><span class="text-secondary font-monospace small">' + displayTime + '</span></td>' +
                    '</tr>';
                }).join('');
            } catch(e) { console.error(e); }
        }

        async function refreshPipelines() {
            try {
                const res = await fetch('/api/tasks');
                const tasks = await res.json();
                
                // 核心改動：將抓取到的資料丟給持久化同步器
                const isUpdated = syncTasksToHistory(tasks);
                if (isUpdated) {
                    renderHistoryUI();
                }

                const box = document.getElementById('pipelineBox');
                // 網頁前端篩選：上方面版只顯示「正在執行 (running/processing)」的活躍任務
                const activeTasks = tasks.filter(t => t.status === 'running' || t.status === 'processing');

                if(!activeTasks || activeTasks.length === 0) { 
                    box.innerHTML = '<span class="text-muted">目前無活躍中任務 (已完成或失敗的任務請見下方歷史存檔)</span>'; 
                    return; 
                }

                box.innerHTML = activeTasks.map(function(t) {
                    let color = 'bg-primary';

					// 🛠️ 優化：如果任務進度已經達到 100% (例如被跳過或在收尾階段)，進度條直接顯示綠色 (bg-success)
					if (t.percent === 100) {
						color = 'bg-success';
					} else if (t.status === 'failed') {
						color = 'bg-danger';
					}

                    let stageInfo = ' <br><small class="text-secondary">當前步驟: <strong>' + (t.stage || '調度中') + '</strong></small>';

                    return '<div class="mb-3 border-bottom pb-2">' +
                        '<div class="d-flex justify-content-between font-weight-bold">' +
                            '<span>📁 ' + t.file_path + ' <span class="badge bg-light text-dark font-monospace">' + t.type.toUpperCase() + '</span></span>' +
							// 如果百分比是 100, Badge 顯示 SUCCESS 會比 PROCESSING 更精準
            				'<span class="badge ' + (t.percent === 100 ? 'bg-success' : 'bg-info') + '">' + (t.percent === 100 ? 'SUCCESS' : t.status.toUpperCase()) + '</span>' +
                        '</div>' +
                        stageInfo +
                        '<div class="progress mt-2" style="height: 18px;">' +
                            '<div class="progress-bar progress-bar-striped progress-bar-animated ' + color + '" role="progressbar" style="width: ' + (t.percent || 50) + '%">' +
                                (t.percent ? t.percent+'%' : '處理中') +
                            '</div>' +
                        '</div>' +
                    '</div>';
                }).join('');
            } catch(e) { console.error(e); }
        }
			
		// 2. 🔥 關鍵修正：將定時器與初始化移入 DOMContentLoaded 中，確保 HTML 載入完畢才執行
		document.addEventListener('DOMContentLoaded', function() {
			refreshFiles(); 
			renderHistoryUI();
			refreshPipelines();
			
			setInterval(refreshFiles, 4000);
			setInterval(refreshPipelines, 1500);
		});
    </script>
</body>
</html>
`