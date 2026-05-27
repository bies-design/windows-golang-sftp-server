package methods

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"
	"strings"

	"github.com/spf13/viper"
	"intelligent-bim-data-conversion-hub/utilities"
)

// --- Rhino Compute API 專用請求結構體 ---

type RhinoComputePayload struct {
	Algo       interface{}  `json:"algo"`
	Pointer    string       `json:"pointer"`
	CacheSolve bool         `json:"CacheSolve"`
	Values     []RhinoValue `json:"values"`
}

type RhinoValue struct {
	ParamName string                 `json:"ParamName"`
	InnerTree map[string][]TreeData  `json:"InnerTree"`
}

type TreeData struct {
	Type string      `json:"Type"`
	Data interface{} `json:"Data"`
}

// --- Rhino Compute API 回傳回應解算結構體 ---

type RhinoComputeResponse struct {
	Values []RhinoResponseValue `json:"values"`
}

type RhinoResponseValue struct {
	ParamName string                           `json:"ParamName"`
	InnerTree map[string][]ResponseTreeData    `json:"InnerTree"`
}

type ResponseTreeData struct {
	Type string `json:"type"`
	Data string `json:"data"` // 因為結果描述一定是字串，這裡可以直接宣告為 string
}

// CallRhinoCompute 負責將實體 3dm 檔案透過 HTTP POST 送至 Rhino Compute 進行解算轉檔
func CallRhinoCompute(pathType string, inputPath string, outputPath string) error {
	computeURL := viper.GetString("RHINO_COMPUTE_URL")
	if computeURL == "" {
		computeURL = "http://localhost:8000/grasshopper" 
	}
	
	// 基本上使用預設值即可，除非有特殊需求才修改
	scriptPath := viper.GetString("RHINO_SCRIPT_PATH")
	if scriptPath == "" {
		scriptPath = "D:/Windows Application/Service/scripts/rhino8_to_glb02.gh"
	}

	// 根據不同的 pathType 進行路徑預處理
	var finalInput, finalOutput string
	switch pathType {
	case "file":
		// 本地檔案相對路徑：自動補上預設前綴根目錄
		// 使用 filepath.Join 組合後，再用 filepath.ToSlash 確保斜線方向與 Windows/Grasshopper 相容

		// 1. 剝離所有目錄偽裝，強制抓出「純檔案名稱 + 副檔名」 (例如 抓出 "Machinery.3dm")
		inputBase := filepath.Base(inputPath)
		outputBase := filepath.Base(outputPath)

		// 2. 抓出副檔名 (例如 ".3dm" 與 ".glb")
		inputExt := filepath.Ext(inputBase)
		outputExt := filepath.Ext(outputBase)

		// 3. 抓出不含副檔名的純主檔名 (用於修復或校正)
		inputPureName := inputBase[:len(inputBase)-len(inputExt)]
		outputPureName := strings.TrimSpace(outputBase[:len(outputBase)-len(outputExt)])

		// 4. 【輸入防呆】檢查副檔名是否為 .3dm (不分大小寫)
		if strings.ToLower(inputExt) != ".3dm" {
			// 如果使用者沒給副檔名或給錯了，我們幫他強制導正為 .3dm
			inputBase = inputPureName + inputExt + ".3dm" 
			// 如果本來就沒副檔名，len(inputExt)為0，這樣能完美補上 .3dm
			if inputExt == "" {
				inputBase = inputPureName + ".3dm"
			}
		}

		// 5. 【輸出防呆】確保輸出一定有 .glb 副檔名
		if strings.ToLower(outputExt) != ".glb" {
			if outputExt == "" {
				outputBase = outputPureName + ".glb"
			} else {
				// 如果使用者傳了 Machinery02.3dm 當輸出，幫他把錯誤的副檔名換成 .glb
				outputBase = outputPureName + ".glb"
			}
		}

		// 6. 安全地拼接到預設的硬編碼目錄
		finalInput = filepath.ToSlash(filepath.Join("D:/3DModels/3dm/", inputBase))
		finalOutput = filepath.ToSlash(filepath.Join("D:/3DModels/glb/", outputBase))

	case "path":
		// 本地檔案絕對路徑：直接轉換斜線
		finalInput = filepath.ToSlash(inputPath)
		finalOutput = filepath.ToSlash(outputPath)

	case "url":
		// 遠端檔案 URL：保持原樣，不使用 filepath 處理以免破壞 URL 協定
		finalInput = inputPath
		// 輸出通常仍為本地端產出路徑，依絕對路徑規格處理
		finalOutput = filepath.ToSlash(outputPath)

	default:
		// 預防萬一的降級極端狀況處理
		finalInput = filepath.ToSlash(inputPath)
		finalOutput = filepath.ToSlash(outputPath)
	}

	// 關鍵路徑轉換：確保格式與 Windows / Grasshopper 相容
	formattedInput := fmt.Sprintf(`"%s"`, filepath.ToSlash(finalInput))
	formattedOutput := fmt.Sprintf(`"%s"`, filepath.ToSlash(finalOutput))

	// 組裝 JSON 樹狀結構
	payload := RhinoComputePayload{
		Algo:       nil,
		Pointer:    scriptPath,
		CacheSolve: false,
		Values: []RhinoValue{
			{
				ParamName: "input_path",
				InnerTree: map[string][]TreeData{
					"{0}": {{Type: "System.String", Data: formattedInput}},
				},
			},
			{
				ParamName: "output_path",
				InnerTree: map[string][]TreeData{
					"{0}": {{Type: "System.String", Data: formattedOutput}},
				},
			},
			{
				ParamName: "convert_precision",
				InnerTree: map[string][]TreeData{
					"{0}": {{Type: "System.Double", Data: 0.6}},
				},
			},
			{
				ParamName: "is_double_sided",
				InnerTree: map[string][]TreeData{
					"{0}": {{Type: "System.Boolean", Data: false}},
				},
			},
		},
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化 Rhino Compute 請求失敗: %v", err)
	}

	utilities.Info("🔍 呼叫 Rhino Compute 進行轉換:")
	utilities.Info("輸入路徑: %s", formattedInput)
    utilities.Info("輸出路徑: %s", formattedOutput)
	utilities.Debug("📡 Post Data Raw (json): %s", string(jsonData))

	client := &http.Client{
		Timeout: 90 * time.Second, // Rhino Compute 可能需要較長時間處理大型檔案
	}

	req, err := http.NewRequest("POST", computeURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("建立 HTTP 請求物件失敗: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("發送連線至 Rhino Compute 伺服器失敗: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, ioErr := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Rhino Compute 錯誤回應 (狀態碼 %d): %s", resp.StatusCode, string(bodyBytes))
	} else if ioErr != nil {
		return fmt.Errorf("讀取 Rhino Compute Server 回應內容失敗: %v", ioErr)
	} else {
		// 🟢 核心安全修正：開始進行業務邏輯層的 JSON 解析與檢查
		var computeResp RhinoComputeResponse

		if err := json.Unmarshal(bodyBytes, &computeResp); err != nil {
			return fmt.Errorf("無法解析 Rhino Compute 的回應 JSON: %v", err)
		}

		// 巡檢回傳的 values 陣列，找出關鍵的輸出欄位 "result"
		var executionResult string
		var foundResultField bool = false
		for _, val := range computeResp.Values {
			if val.ParamName == "result" {
				// 🟢 修正防禦：如果 InnerTree 是空的，代表上游元件連動都沒動就死鎖或斷線了
				if len(val.InnerTree) == 0 {
					rawJSONStr := string(bodyBytes)
					executionResult = "轉檔腳本未輸出任何數據 (InnerTree 為空)。 回應內容: %s" + rawJSONStr
					foundResultField = true // 雖然沒拿到預期的結果，但至少找到了對應的輸出欄位，避免重複報錯
					break
				}

				// 取出樹狀分支 "{0}" 中的第一個節點資料
				if treeBranch, exists := val.InnerTree["{0}"]; exists && len(treeBranch) > 0 {
					executionResult = treeBranch[0].Data
					foundResultField = true
					break
				}
			}
		}

		// 驗證 1：防呆，如果連對應的輸出欄位都沒找到，代表腳本配置有誤
		if !foundResultField {
			return fmt.Errorf("轉檔異常：Rhino Compute 回應中缺漏 'result' 輸出欄位, 反饋內容: %s", string(bodyBytes))
		}

		// 驗證 2：精確檢查是否包含 C# 內定義的「轉檔成功」字串
		// 註：因為回傳值可能帶有 JSON 序列化遺留的轉義雙引號 (如 "\"轉檔成功\"")，使用 strings.Contains 最穩健
		if !strings.Contains(executionResult, "轉檔成功") {
			// 這裡會直接把 C# 拋出的 "執行發生嚴重異常: ... at Rhino.RhinoDoc.Import" 完整字串捕獲並回報給 Pipeline
			return fmt.Errorf("轉檔管線業務邏輯失敗，伺服器反饋：\n%s", executionResult)
		}
		
		// 真正大功告成
		utilities.Info("✅ Rhino Compute 轉檔任務完全成功！反饋資訊: %s", executionResult)
	}

	return nil
}