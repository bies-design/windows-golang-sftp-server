package services

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"net"

	"github.com/pkg/sftp"
	"github.com/spf13/viper"
	"golang.org/x/crypto/ssh"

	"intelligent-bim-data-conversion-hub/models"
	"intelligent-bim-data-conversion-hub/utilities"
)

type SFTPServer struct {
	port     string
	dataDir  string
	taskMgr  *models.Manager
	listener net.Listener
}

func NewSFTPServer(port string, dataDir string, taskMgr *models.Manager) *SFTPServer {
	return &SFTPServer{
		port:    port,
		dataDir: dataDir,
		taskMgr: taskMgr,
	}
}

func (ss *SFTPServer) Start(ctx context.Context) {
	maxUploads := viper.GetInt("MAX_UPLOADS")
	maxDownloads := viper.GetInt("MAX_DOWNLOADS")

	backend := models.NewCustomSFTPBackend(ss.dataDir, ss.taskMgr, maxUploads, maxDownloads)
	handlers := sftp.Handlers{
		FileGet:  backend,
		FilePut:  backend,
		FileCmd:  backend,
		FileList: backend,
	}

	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	signer, _ := ssh.NewSignerFromKey(key)
	// config := &ssh.ServerConfig{NoClientAuth: true}
	// 修正：廢棄 NoClientAuth，改用萬用密碼驗證回呼（PasswordCallback）
	// 這樣不論圖形化工具送出什麼帳號、密碼，伺服器都會正常回傳「通過」，避免工具無所適從
	config := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			utilities.Info("[SFTP Auth] 使用者 %s 正在嘗試登入", c.User())
			return nil, nil
		},
	}
	config.AddHostKey(signer)

	var err error
	ss.listener, err = net.Listen("tcp", ":"+ss.port)
	if err != nil {
		utilities.Error("❌ [SFTP] 無法監聽 Port: %v", err)
		return
	}
	utilities.Info("🟢 [SFTP] 伺服器已啟動, 監聽 Port: %s 儲存目錄: %s", ss.port, ss.dataDir)

	// 背景處理優雅關閉監聽
	go func() {
		<-ctx.Done()
		if ss.listener != nil {
			ss.listener.Close()
			utilities.Info("🟥 [SFTP] 監聽器已停止接收新連線")
		}
	}()

	for {
		nConn, err := ss.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return // 屬於正常關閉
			default:
				utilities.Info("❌ [SFTP] 接受 TCP 連線失敗: %v", err)
				continue
			}
		}

		go ss.handleSSHConnection(nConn, config, handlers)
	}
}

func (ss *SFTPServer) handleSSHConnection(conn net.Conn, config *ssh.ServerConfig, handlers sftp.Handlers) {
	_, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		utilities.Info("❌ [SFTP] SSH 握手失敗: %v", err)
		return
	} else {
		utilities.Info("✅ [SFTP] SSH 握手成功，來自: %s", conn.RemoteAddr())
	}
	go ssh.DiscardRequests(reqs) // 忽略所有全局請求，避免被舊請求干擾

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
}