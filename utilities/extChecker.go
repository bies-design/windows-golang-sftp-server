package utilities

import (
	"strings"
)

// hasAllowedExt 檢查檔名是否符合指定的副檔名
func hasAllowedExt(filename string, allowed map[string]bool) bool {
	lowerName := strings.ToLower(filename)

	// 1. 優先檢查複合式副檔名 (例如 .tar.gz, .tar.bz2)
	if strings.HasSuffix(lowerName, ".tar.gz") && allowed["tar.gz"] {
		return true
	}
	if strings.HasSuffix(lowerName, ".tar.bz2") && allowed["tar.bz2"] {
		return true
	}

	// 2. 檢查一般的單一副檔名 (例如 .frag, .zip, .gz, .bz2)
	// 找到最後一個 '.' 的位置
	if idx := strings.LastIndex(lowerName, "."); idx != -1 && idx < len(lowerName)-1 {
		ext := lowerName[idx+1:]
		return allowed[ext]
	}

	return false
}

// 提供給外部 package 呼叫的介面
func HasAllowedExt(filename string, allowedExts map[string]bool) bool {
	return hasAllowedExt(filename, allowedExts)
}