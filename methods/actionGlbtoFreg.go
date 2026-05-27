package methods

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"path/filepath"

	"github.com/spf13/viper"
	"intelligent-bim-data-conversion-hub/utilities"
)

// --- 請求與回應的數據結構定義 ---

// GlbToFragRequest 定義發送給 B 電腦 Node API 的 Payload 結構
type GlbToFragRequest struct {
	InputPath string `json:"input_path"`
	OutputDir string `json:"output_dir"`
}

// GlbToFragResponse 定義 B 電腦 Node API 回傳的原子化 JSON 結構
type GlbToFragResponse struct {
	Status  string `json:"status"`  // "completed" 或 "failed" 或 "error"
	Message string `json:"message"` // 轉檔成功/失敗的摘要描述
	Stdout  string `json:"stdout"`  // Node.js 腳本在終端機輸出的真實日誌
	Error   string `json:"error"`   // 若發生例外，Node.js 拋出的錯誤訊息
	Stderr  string `json:"stderr"`  // Node.js 腳本的標準錯誤輸出
	ZipType string `json:"ziptype"` // 壓縮包類型，例如 "bzip2" 或 "zip"
	FragResult string `json:"fragresult"` // Frag 轉換結果
}

// 統一回報格式，避免錯誤發生，封閉修改範圍，讓外部呼叫者更容易處理轉檔結果
type GlbToFragResult struct {
	FragResult string
	CompressionFilePath string // 新增一個欄位用於回傳壓縮包的路徑，如果有的話
	Error error
}

func returnResult(err error, fragResult string, compressionFilePath string) GlbToFragResult {
	// 調整為 Go 社群的開發習慣（Idomatic Go）, 把 error 放在最後一個回傳值
	return GlbToFragResult{
		FragResult: fragResult,
		CompressionFilePath: compressionFilePath,
		Error: err,
	}
}

// CallGlbtoFreg 負責將轉檔請求發送至遠端 B 電腦的 Caddy / Node 服務並驗證結果
// 註：因為檔案本來就都在 B 電腦，此處傳入的 glbPath 與 outputDirPath 皆為 B 電腦上的絕對實體路徑
func CallGlbtoFreg(glbPath string, outputDirPath string) GlbToFragResult {
	// 1. 配置遠端 B 電腦的 Caddy 門戶 API 網址 (可配合專案改由 Viper 環境變數讀取)
	// 假設 B 電腦 IP 監聽 Port 為 8089，對應 Caddyfile 設定的 /api/convert 路由
	remoteURL := viper.GetString("FRAGMENTS_API_URL")
	if remoteURL == "" {
		remoteURL = "http://localhost:8089/api/convert"
	}

	// 2. 打包請求 Payload
	requestPayload := GlbToFragRequest{
		InputPath: glbPath,
		OutputDir: outputDirPath,
	}

	jsonData, err := json.Marshal(requestPayload)
	if err != nil {
		return returnResult(fmt.Errorf("GLB 轉檔請求 JSON 序列化失敗: %v", err), "", "")
	}

	// 3. 建立具備安全超時機制的 HTTP 請求 (Frag 轉換大模型可能耗時較長，設定 5 分鐘超時)
	client := &http.Client{
		Timeout: 5 * time.Minute,
	}

	utilities.Info("[🔗 Frag 轉檔] 正在發送轉檔請求至服務: %s\nPayload: %s", remoteURL, string(jsonData))
	req, err := http.NewRequest("POST", remoteURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return returnResult(fmt.Errorf("建立 HTTP 轉檔請求失敗: %v", err), "", "")
	}
	req.Header.Set("Content-Type", "application/json")

	// 4. 正式發送請求
	resp, err := client.Do(req)
	if err != nil {
		return returnResult(fmt.Errorf("無法連線至遠端 Frag 轉檔伺服器 (%s): %v", remoteURL, err), "", "")
	}
	defer resp.Body.Close()

	utilities.Info("[🔗 Frag 轉檔] 收到服務回應: 狀態碼 %d", resp.StatusCode)
	// 5. 讀取回應內容
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return returnResult(fmt.Errorf("讀取 Frag 伺服器回應內容失敗: %v", err), "", "")
	}

	// 6. 解算 Node 服務回傳的業務 JSON
	var nodeResp GlbToFragResponse
	if err := json.Unmarshal(bodyBytes, &nodeResp); err != nil {
		return returnResult(fmt.Errorf("無法解析 Frag 伺服器回應的 JSON (原始內容: %s): %v", string(bodyBytes), err), "", "")
	}

	// 7. 二次防禦：網路層級狀態碼檢查
	// 即使 Node 內部崩潰 (500)，我們也要先把 Body 抓下來解析詳細錯誤原因，而不是只丟出 500 錯誤
	if resp.StatusCode != http.StatusOK || nodeResp.Status == "failed" || nodeResp.Status == "error" {
		utilities.Warn("[⚠️ Frag 轉檔失敗] HTTP 狀態碼: %d, Node 服務回傳狀態: %s, 訊息: %s", resp.StatusCode, nodeResp.Status, nodeResp.Message)
		
		// 優先順序 1：如果 Node.js 有拋出系統層級 error (例如 Pipeline 執行失敗)
		if nodeResp.Status == "failed" {
			return returnResult(fmt.Errorf("Frag 伺服器任務執行失敗 錯誤: %s (詳細 Stderr: %s) (過程 Stdout: %s)", nodeResp.Error, nodeResp.Stderr, nodeResp.Stdout), "", "")
		}
		
		// 優先順序 2：如果是路由找不到或參數缺失的 message (例如 404 或 400)
		if nodeResp.Status == "error" {
			return returnResult(fmt.Errorf("Frag 伺服器拒絕請求 (狀態碼 %d): %s", resp.StatusCode, nodeResp.Message), "", "")
		}

		// 備援：如果什麼欄位都沒寫，但狀態碼不對
		return returnResult(fmt.Errorf("Frag 伺服器網路層級錯誤 (狀態碼 %d): %s", resp.StatusCode, string(bodyBytes)), "", "")
	}


	// 8. 🟢 核心原子業務判定：精確檢查 status 是否為 "completed"
	var compressionFilePath string = "" // 預設為空，除非轉檔成功且 Node 服務回傳了 fragresult 欄位
	if !strings.EqualFold(nodeResp.Status, "completed") {
		// 整合 Node 丟出來的各種錯誤蛛絲馬跡，回傳給 Go 的 Pipeline 狀態機
		var errBuilder strings.Builder
		errBuilder.WriteString(fmt.Sprintf("Frag 轉檔管線業務邏輯 [%s] -> %s\n", nodeResp.Status, nodeResp.Message))
		
		if nodeResp.Error != "" {
			errBuilder.WriteString(fmt.Sprintf("[Node Error]: %s\n", nodeResp.Error))
		}
		if nodeResp.Stderr != "" {
			errBuilder.WriteString(fmt.Sprintf("[CLI Stderr]: %s\n", nodeResp.Stderr))
		}
		if nodeResp.Stdout != "" {
			errBuilder.WriteString(fmt.Sprintf("[CLI Stdout 殘留殘渣]:\n%s\n", nodeResp.Stdout))
		}
		
		return returnResult(fmt.Errorf(errBuilder.String()), "", "")
	} else  {
		// 轉檔成功，但 stdout 中可能有警告訊息，我們也要記錄下來以供後續分析
		var isCompressionSuccess bool = true // 預設為成功，除非發現錯誤或警告訊息
		if nodeResp.Error != "" {
			utilities.Warn("[⚠️ Frag 轉檔成功，但有警告訊息，表示沒有材質包]:\n%s\n", nodeResp.Error)
			isCompressionSuccess = false
		}
		if nodeResp.Stderr != "" {
			utilities.Warn("[⚠️ Frag 轉檔成功，但有 CLI Stderr 輸出]:\n%s\n", nodeResp.Stderr)
			isCompressionSuccess = false
		}
		if strings.Contains(nodeResp.Stdout, "GLB 轉換成功") && isCompressionSuccess {
			// 1. 取回轉檔的檔案名稱
			base := filepath.Base(nodeResp.FragResult) // 取得輸入路徑的檔名，例如 "model.glb"
			// 2. 去除副檔名，得到純檔名，例如 "model"
			fileNameWithoutExt := strings.TrimSuffix(base, ".frag") // 轉檔後的檔案是 .frag 格式
			// 3. 取得路徑的目錄，例如 "/path/to/output/model.frag" -> "/path/to/output"
			filesDir := filepath.Dir(nodeResp.FragResult) // 取得輸入路徑的目錄，例如 "/path/to/output"
			// 4. 取得壓縮的方案選擇
			zipType := nodeResp.ZipType // 假設 Node 服務會在成功時回傳 ziptype 欄位，表示使用的壓縮方案
			// 5. 組合出轉檔後路徑和材質壓縮包的名稱，例如 "filename.zip"
			compressionFilePath = filesDir + "/" + fileNameWithoutExt + "." + zipType // 假設 Node 服務會在成功時回傳 fragresult 欄位，和壓縮檔的路徑相同
			utilities.Info("[✅ Frag 轉檔成功，且材質包壓縮成功]:\nFrag Result: %s\nCompression File Path: %s\n", nodeResp.FragResult, compressionFilePath)
		}

	}

	// 9. 轉檔完全成功，將 Node.js 內部的轉檔日誌有條理地反映到 Go 的終端機中
	utilities.Debug("\n[✅ Frag 轉檔完全成功]\n--- Node.js 執行階段輸出 (stdout) ---\n%s\n------------------------------------\n", nodeResp.Stdout)
	return returnResult(nil, nodeResp.FragResult, compressionFilePath)
}