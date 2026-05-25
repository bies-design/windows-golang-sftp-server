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
	CacheSolve bool         `json:"cachesolve"`
	Values     []RhinoValue `json:"values"`
}

type RhinoValue struct {
	ParamName string                 `json:"ParamName"`
	InnerTree map[string][]TreeData  `json:"InnerTree"`
}

type TreeData struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
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
		scriptPath = "D:/Windows Application/Service/scripts/rhino8_to_glb.gh"
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
				ParamName: "RH_IN:input_path",
				InnerTree: map[string][]TreeData{
					"{0}": {{Type: "System.String", Data: formattedInput}},
				},
			},
			{
				ParamName: "RH_IN:output_path",
				InnerTree: map[string][]TreeData{
					"{0}": {{Type: "System.String", Data: formattedOutput}},
				},
			},
			{
				ParamName: "RH_IN:convert_precision",
				InnerTree: map[string][]TreeData{
					"{0}": {{Type: "System.Double", Data: 0.6}},
				},
			},
			{
				ParamName: "RH_IN:is_double_sided",
				InnerTree: map[string][]TreeData{
					"{0}": {{Type: "System.Boolean", Data: false}},
				},
			},
		},
	}

	utilities.Debug("🔍 呼叫 Rhino Compute 進行轉換:\n輸入: %s\n輸出: %s\n", formattedInput, formattedOutput)
	utilities.Debug("📡 Post Data Raw (json): %+v\n", payload)

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化 Rhino Compute 請求失敗: %v", err)
	}

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

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Rhino Compute 錯誤回應 (狀態碼 %d): %s", resp.StatusCode, string(bodyBytes))
	} else {
		utilities.Debug("✅ Rhino Compute 成功回應: %s\n", string(bodyBytes))
	}

	return nil
}