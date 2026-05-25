package main

import (
	"testing"
)

// 建立一個測試跳板，直接呼叫你 main.go 裡的 main 函式
func TestRunServer(t *testing.T) {
	main()
}