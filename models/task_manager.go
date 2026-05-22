package models

import (
	"fmt"
	"sync"
	"time"
	"os"
	"path/filepath"

	"sftp-server/methods"
)

// Task 記錄單一上傳或下載任務的狀態 (公開欄位以利 JSON 序列化)
type Task struct {
	ID         string `json:"id"`
	TaskType   string `json:"type"` // "upload", "download", "pipeline"
	FilePath   string `json:"file_path"`
	BytesTrans int64  `json:"bytes_transferred"`
	Status     string `json:"status"` // "running", "processing", "completed", "failed"
	Stage      string    `json:"stage,omitempty"`
	Percent    int       `json:"percent,omitempty"`
	ErrorMsg   string    `json:"error_msg,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
	ConvertedAt string    `json:"converted_at,omitempty"`
}

// Manager 負責管理所有運行中的任務 (支援併發安全)
type Manager struct {
	mu    sync.RWMutex
	tasks map[string]*Task
	lastMtimes map[string]time.Time // 記錄每個完整路徑檔案上一次執行的修改時間
}

// NewManager 初始化並回傳一個全新的 Manager 實例
func NewManager() *Manager {
	return &Manager{
		tasks: make(map[string]*Task),
		lastMtimes: make(map[string]time.Time), // 初始化修改時間記錄
	}
}

// ShouldProcessFile 檢查檔案修改時間是否與上一次不同
// 若相同回傳 false (跳過)；若不同或新檔案則更新時間並回傳 true (允許執行)
func (tm *Manager) ShouldProcessFile(fullPath string, currentMtime time.Time) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	lastMtime, exists := tm.lastMtimes[fullPath]
	// 如果紀錄存在，且時間完全相同，代表檔案沒變，拒絕重新進入 pipeline
	if exists && lastMtime.Equal(currentMtime) {
		return false
	}

	// 時間不同或者是全新上傳的檔案，更新紀錄並放行
	tm.lastMtimes[fullPath] = currentMtime
	return true
}

// AddTask 新增一個任務到管理器中
func (tm *Manager) AddTask(t *Task) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t.UpdatedAt = time.Now()
	tm.tasks[t.ID] = t
}

// UpdateProgress 依據傳入的 byte 數量，累加特定任務的傳輸進度
func (tm *Manager) UpdateProgress(id string, n int64) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if t, exists := tm.tasks[id]; exists {
		t.BytesTrans += n
		t.UpdatedAt = time.Now()
	}
}

// UpdateStatus 更新特定任務的最終狀態
func (tm *Manager) UpdateStatus(id string, status string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if t, exists := tm.tasks[id]; exists {
		t.Status = status
		t.UpdatedAt = time.Now()
	}
}

// UpdatePipeline 用於精準更新三階段流水線的進度
func (tm *Manager) UpdatePipeline(id string, stage string, percent int, status string, errMsg string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if t, exists := tm.tasks[id]; exists {
		t.Stage = stage
		t.Percent = percent
		t.Status = status
		t.ErrorMsg = errMsg
		t.UpdatedAt = time.Now()
	}
}

func (tm *Manager) GetTask(id string) (*Task, bool) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	t, exists := tm.tasks[id]
	return t, exists
}

// GetAllTasks 取得當前所有任務的快照列表 (供 API 統合回應使用)
func (tm *Manager) GetAllTasks() []Task {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	list := make([]Task, 0, len(tm.tasks))
	for _, t := range tm.tasks {
		list = append(list, *t)
	}
	return list
}

// GenerateTaskID 依據任務類型產生不重複的唯一識別碼
func GenerateTaskID(taskType string) string {
	prefix := "UNK"
	switch taskType {
	case "upload":
		prefix = "UP"
	case "download":
		prefix = "DL"
	case "pipeline":
		prefix = "PL"
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// StartPipeline 核心功能 ：執行三階段 BIM 轉換與上傳流水線
func (tm *Manager) StartPipeline(taskID string, fullPath string) {
    // 關鍵 1：稍微延遲 200 毫秒，給 SFTP 客戶端的 Setstat 指令完成寫入時間的空檔
	time.Sleep(200 * time.Millisecond)

	// 關鍵 2：讀取實體檔案在硬碟上的最新狀態
	fi, err := os.Stat(fullPath)
	if err != nil {
		tm.UpdatePipeline(taskID, "讀取檔案狀態失敗", 0, "failed", fmt.Sprintf("無法獲取檔案資訊: %v", err))
		return
	}
	currentMtime := fi.ModTime()

	// 關鍵 3：比對修改時間，相同就優雅跳過，不同才往下跑 Pipeline
	if !tm.ShouldProcessFile(fullPath, currentMtime) {
		tm.mu.Lock()
		if t, exists := tm.tasks[taskID]; exists {
			t.Stage = "已完成 (內容未變更，跳過轉換)"
			t.Percent = 100
			t.Status = "processing"
			// 如果跳過，轉換時間直接繼承當前，或是您可以保持空白
			t.ConvertedAt = time.Now().Format("2006-01-02 15:04:05") 
			t.UpdatedAt = time.Now()
		}
		tm.mu.Unlock()
		
		// 開啟一個輕量級協程 (Goroutine)，讓它在即時看板上停留 5 秒，再正式歸檔移出
		go func() {
			time.Sleep(5 * time.Second)
			tm.UpdateStatus(taskID, "completed")
		}()
		return
	}

	// 解析出純檔名（不含副檔名）以利路徑分流
	baseDir := filepath.Dir(fullPath)
	fileName := filepath.Base(fullPath)
	ext := filepath.Ext(fileName)
	pureName := fileName[:len(fileName)-len(ext)]

	// ✨ 新增調整：將產出檔案引導至專屬子資料夾 toGlb 與 frag
	glbPath := filepath.Join(baseDir, "toGlb", pureName+".glb")
	zipPath := filepath.Join(baseDir, "frag", pureName+"_processed.zip")

	// === 階段 1: 3dm to glb (rhino.compute) ===
	tm.UpdatePipeline(taskID, "1. 3dm to glb (rhino.compute)", 10, "processing", "")
	if err := callRhinoCompute("file", fullPath, glbPath); err != nil {
		tm.UpdatePipeline(taskID, "1. 3dm to glb (rhino.compute)", 10, "failed", fmt.Sprintf("Rhino 轉換失敗: %v", err))
		return
	}
	tm.UpdatePipeline(taskID, "1. 3dm to glb (rhino.compute)", 100, "processing", "")
	time.Sleep(800 * time.Millisecond) // 轉場平滑感

	// === 階段 2: glb to fragment+材質包 (thatopen, nodejs project) ===
	tm.UpdatePipeline(taskID, "2. glb to fragment+材質包 (thatopen)", 20, "processing", "")
	if err := callThatOpenConverter(glbPath, zipPath); err != nil {
		tm.UpdatePipeline(taskID, "2. glb to fragment+材質包 (thatopen)", 20, "failed", fmt.Sprintf("ThatOpen 轉換失敗: %v", err))
		return
	}
	tm.UpdatePipeline(taskID, "2. glb to fragment+材質包 (thatopen)", 100, "processing", "")
	time.Sleep(800 * time.Millisecond)

	// === 階段 3: frag. + 材質包壓縮檔案 上傳到 seaweedfs ===
	tm.UpdatePipeline(taskID, "3. 上傳至 SeaweedFS", 10, "processing", "")
	if err := uploadToSeaweedFS(zipPath); err != nil {
		tm.UpdatePipeline(taskID, "3. 上傳至 SeaweedFS", 10, "failed", fmt.Sprintf("SeaweedFS 上傳失敗: %v", err))
		return
	}
	
	// 全部完成
	tm.mu.Lock()
	if t, exists := tm.tasks[taskID]; exists {
		t.Stage = "已完成"
		t.Percent = 100
		t.Status = "processing"
		t.ConvertedAt = time.Now().Format("2006-01-02 15:04:05") // 寫入清晰的轉換成功時間
		t.UpdatedAt = time.Now()
	}
	tm.mu.Unlock()

	go func() {
		time.Sleep(5 * time.Second)
		tm.UpdateStatus(taskID, "completed")
	}()
}

// --- 外部服務中介聯結介面 (預留實作骨架) ---
func callRhinoCompute(pathType string, inputPath string, outputPath string) error {
	// ✨ 核心調整：直接呼叫獨立封裝的解算方法，未來更換成其他解算器只需在 methods 修改
	/**
	* parameters:
	* - pathType: "file" (本地檔案相對路徑)，"path" (本地檔案絕對路徑) 或 "url" (遠端檔案 URL)
	* - inputPath: 3dm 檔案的檔名、完整路徑 (本地) 或 URL (遠端)，例如 "C:/data/model.3dm" 或 "https://example.com/model.3dm"
	* - outputPath: 轉換後 glb 檔案的檔名、完整路徑 (本地) 或 URL (遠端)，例如 "C:/data/toGlb/model.glb" 或 "https://example.com/model.glb"
	*/
	return methods.CallRhinoCompute(pathType, inputPath, outputPath)
}

func callThatOpenConverter(glbPath string, outputZipPath string) error {
	// 實際操作：使用 exec.Command("node", "convert.js", glbPath) 調用 Node.js 服務
	time.Sleep(2 * time.Second)
	return nil
}

func uploadToSeaweedFS(zipPath string) error {
	// 實際操作：將產出的壓縮材質包上傳到 SeaweedFS 分散式儲存
	time.Sleep(1 * time.Second)
	return nil
}