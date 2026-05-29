package utilities

import (
	"fmt"
	"os"
	"time"
	"strings"
)

// 1. 定義您的自訂日誌介面 (Interface)
// 這能讓您的商務邏輯與具體實作脫鉤，方便未來更換或寫單元測試
type MyLogger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// 2. 定義日誌等級常數
type LogLevel int
const (
	LevelDebug LogLevel = iota
	LevelInfo
	LevelWarn
	LevelError
)

// 全域共用的日誌實例 (未初始化前預設使用 slog 的預設值，避免 nil 崩潰)
var currentLevel LogLevel = LevelInfo // 預設安全牌
var customNewline string = "\n"
var autoNewline bool = true

// 3. 動態修改除錯顯示等級的方法
/**
* levelString = { 'debug', 'info', 'warn', 'error' } 這些都是預定義的等級常數
*/
func getLevelByString(levelString string) LogLevel {
	switch strings.ToUpper(strings.TrimSpace(levelString)) {
	case "DEBUG":
		return LevelDebug
	case "INFO":
		return LevelInfo
	case "WARN", "WARNING":
		return LevelWarn
	case "ERROR":
		return LevelError
	default:
		return LevelInfo // 預設安全牌
	}
}
func SetLevel(level LogLevel) {
	currentLevel = level
}
func GetLevel() LogLevel {
	return currentLevel
}
// ✨ 新增：允許動態調整全域換行行為的方法
func SetAutoNewline(enabled bool, customNewlineChar string) {
	autoNewline = enabled
	if customNewlineChar != "" {
		customNewline = customNewlineChar
	}
}

// 4. InitLogger 初始化全域日誌，通常在 main.go 的 init() 或 main() 一開始執行
func InitLogger(initialLevel any, autoNewlineEnabled bool , customNewlineChar string ) {

	switch v := initialLevel.(type) {
	case LogLevel:
		SetLevel(v)
	case string:
		SetLevel(getLevelByString(v))
	default:
		SetLevel(LevelInfo) // 預設安全牌
	}

	if autoNewlineEnabled {
		SetAutoNewline(autoNewlineEnabled, customNewlineChar)
	}	

}

// 5. 實作介面方法
// ✨ 內部輔助防護函式：檢查並根據參數決定是否在 msg 尾端補上換行符號
func logOutput(levelLabel string, msg string, args ...any) {
	// 先使用 fmt.Sprintf 處理商務邏輯中的 %s, %v 替換，解決 %s 未整併的問題
	formattedMsg := msg
	if len(args) > 0 {
		formattedMsg = fmt.Sprintf(msg, args...)
	}

	// 檢查並追加換行符號
	if autoNewline && !strings.HasSuffix(formattedMsg, customNewline) {
		formattedMsg += customNewline
	}

	// 取得當前時間（符合你輸出的時間格式）
	timeStr := time.Now().Format("2006-05-02T15:04:05.000+08:00")

	// 乾淨輸出：完全去除 key=，回復直觀的日誌排版
	fmt.Fprintf(os.Stdout, "%s [%s] %s", timeStr, levelLabel, formattedMsg)
}

func Debug(msg string, args ...any) { 
	if currentLevel <= LevelDebug {
		logOutput("DEBUG", msg, args...) 
	}
}
func Info(msg string, args ...any)  { if currentLevel <= LevelInfo { logOutput("INFO", msg, args...) } }
func Warn(msg string, args ...any)  { if currentLevel <= LevelWarn { logOutput("WARN", msg, args...) } }
func Error(msg string, args ...any) { if currentLevel <= LevelError { logOutput("ERROR", msg, args...) } }

// 6. 在專案其他地方取用logger
