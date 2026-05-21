package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"

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
		http.HandleFunc("/api/tasks", func(w http.ResponseWriter, r *http.Request) {
			tasks := taskMgr.GetAllTasks()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(tasks)
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