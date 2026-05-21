package models

import (
	"fmt"
	"sync"
	"time"
)

// Task 記錄單一上傳或下載任務的狀態 (公開欄位以利 JSON 序列化)
type Task struct {
	ID         string `json:"id"`
	TaskType   string `json:"type"` // "upload" 或 "download"
	FilePath   string `json:"file_path"`
	BytesTrans int64  `json:"bytes_transferred"`
	Status     string `json:"status"` // "running", "completed", "error"
}

// Manager 負責管理所有運行中的任務 (支援併發安全)
type Manager struct {
	mu    sync.RWMutex
	tasks map[string]*Task
}

// NewManager 初始化並回傳一個全新的 Manager 實例
func NewManager() *Manager {
	return &Manager{
		tasks: make(map[string]*Task),
	}
}

// AddTask 新增一個任務到管理器中
func (tm *Manager) AddTask(t *Task) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.tasks[t.ID] = t
}

// UpdateProgress 依據傳入的 byte 數量，累加特定任務的傳輸進度
func (tm *Manager) UpdateProgress(id string, n int64) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if t, exists := tm.tasks[id]; exists {
		t.BytesTrans += n
	}
}

// UpdateStatus 更新特定任務的最終狀態
func (tm *Manager) UpdateStatus(id string, status string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if t, exists := tm.tasks[id]; exists {
		t.Status = status
	}
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
	if taskType == "upload" {
		prefix = "UP"
	} else if taskType == "download" {
		prefix = "DL"
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}