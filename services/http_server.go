package services

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"intelligent-bim-data-conversion-hub/models"
	"intelligent-bim-data-conversion-hub/utilities"
)

type HTTPServer struct {
	server  *http.Server
	dataDir string
	taskMgr *models.Manager // 請根據你真實的 TaskManager 型態調整，如果是 interface 請替換
	assets  embed.FS
}

func NewHTTPServer(port string, dataDir string, taskMgr *models.Manager, assets embed.FS) *HTTPServer {
	mux := http.NewServeMux()
	hs := &HTTPServer{
		server: &http.Server{
			Addr:    ":" + port,
			Handler: mux,
		},
		dataDir: dataDir,
		taskMgr: taskMgr,
		assets:  assets,
	}
	hs.registerRoutes(mux)
	return hs
}

func (hs *HTTPServer) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/tasks", func(w http.ResponseWriter, r *http.Request) {
		tasks := hs.taskMgr.GetAllTasks()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tasks)
	})

	mux.HandleFunc("/api/files", func(w http.ResponseWriter, r *http.Request) {
		files, err := os.ReadDir(filepath.Join(hs.dataDir, "3dm"))
		if err != nil {
			http.Error(w, "無法讀取 3dm 目錄", http.StatusInternalServerError)
			return
		}
		type FileItem struct {
			Name    string    `json:"name"`
			Size    int64     `json:"size"`
			ModTime time.Time `json:"mod_time"`
		}
		var list []FileItem
		for _, f := range files {
			if !f.IsDir() && f.Name()[0] != '.' {
				info, err := f.Info()
				if err == nil {
					list = append(list, FileItem{Name: f.Name(), Size: info.Size(), ModTime: info.ModTime()})
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	})

	mux.HandleFunc("/api/files/glb", func(w http.ResponseWriter, r *http.Request) {
		files, err := os.ReadDir(filepath.Join(hs.dataDir, "glb"))
		if err != nil { http.Error(w, "無法讀取 GLB 目錄", http.StatusInternalServerError); return }
		type FileItem struct { Name string `json:"name"`; Size int64 `json:"size"`; ModTime time.Time `json:"mod_time"` }
		var list []FileItem
		for _, f := range files {
			if !f.IsDir() && f.Name()[0] != '.' {
				info, err := f.Info()
				if err == nil { list = append(list, FileItem{Name: f.Name(), Size: info.Size(), ModTime: info.ModTime()}) }
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	})

	mux.HandleFunc("/api/files/frag", func(w http.ResponseWriter, r *http.Request) {
		files, err := os.ReadDir(filepath.Join(hs.dataDir, "frag"))
		if err != nil { http.Error(w, "無法讀取 Frag 目錄", http.StatusInternalServerError); return }

		allowedExts := map[string]bool{
			"frag": true, "bz2": true, "gz": true, "tar.gz": true, "tar.bz2": true, "zip": true,
		}

		type FileItem struct { Name string `json:"name"`; Size int64 `json:"size"`; ModTime time.Time `json:"mod_time"` }
		var list []FileItem

		for _, f := range files {
			name := f.Name()
			if f.IsDir() || len(name) == 0 || name[0] == '.' { continue }
			if !utilities.HasAllowedExt(name, allowedExts) { continue }

			info, err := f.Info()
			if err == nil {
				list = append(list, FileItem{Name: name, Size: info.Size(), ModTime: info.ModTime()})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	})

	mux.HandleFunc("/api/s3-logs", func(w http.ResponseWriter, r *http.Request) {
		vcsLogPath := filepath.Join(hs.dataDir, ".versions", "s3_upload_log.json")
		w.Header().Set("Content-Type", "application/json")
		if data, err := os.ReadFile(vcsLogPath); err == nil {
			w.Write(data)
		} else {
			w.Write([]byte("[]"))
		}
	})

	mux.HandleFunc("/api/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "僅支援 POST 請求", http.StatusMethodNotAllowed)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "無效的檔案欄位", http.StatusBadRequest)
			return
		}
		defer file.Close()

		safeName := filepath.Base(header.Filename)
		fullPath := filepath.Join(hs.dataDir, "3dm/", safeName)

		hs.taskMgr.BackupExistingFile(hs.dataDir, safeName)

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

		taskID := models.GenerateTaskID("pipeline")
		hs.taskMgr.AddTask(&models.Task{
			ID:       taskID,
			TaskType: "pipeline",
			FilePath: safeName,
			Status:   "processing",
		})

		if filepath.Ext(safeName) == ".3dm" {
			go hs.taskMgr.StartPipeline(taskID, fullPath)
		} else {
			hs.taskMgr.UpdateStatus(taskID, "completed")
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"message":"網頁端上傳成功","task_id":"%s"}`, taskID)
	})

	mux.HandleFunc("/api/versions", func(w http.ResponseWriter, r *http.Request) {
		fileName := r.URL.Query().Get("file")
		if fileName == "" { http.Error(w, "缺少 file 參數", http.StatusBadRequest); return }
		
		ext := filepath.Ext(fileName)
		pureName := fileName[:len(fileName)-len(ext)]
		vcsLogPath := filepath.Join(hs.dataDir, ".versions", pureName+"_vcs.json")
		
		w.Header().Set("Content-Type", "application/json")
		if data, err := os.ReadFile(vcsLogPath); err == nil {
			w.Write(data)
		} else {
			w.Write([]byte("[]"))
		}
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if data, err := os.ReadFile("index.html"); err == nil {
			w.Write(data)
		} else {
			if embedData, err := hs.assets.ReadFile("index.html"); err == nil {
				w.Write(embedData)
			} else {
				http.Error(w, "BIM 控制台內嵌網頁加載失敗", http.StatusInternalServerError)
			}
		}
	})
}

func (hs *HTTPServer) Start(ctx context.Context) {
	utilities.Info("🟢 [API] HTTP 伺服器啟動，監聽 Port %s", hs.server.Addr)
	if err := hs.server.ListenAndServe(); err != http.ErrServerClosed {
		utilities.Error("❌ [API] HTTP 伺服器異常中斷: %v", err)
	}
}

func (hs *HTTPServer) Stop(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := hs.server.Shutdown(ctx); err != nil {
		utilities.Error("❌ [API] HTTP 優雅停機失敗: %v", err)
	} else {
		utilities.Info("🟥 [API] HTTP 伺服器已安全關閉")
	}
}

