# windows-golang-Intelligent-BIM-Data-Conversion-Hub
邊緣運行Rhino Compute服務的組件之一
管理核心：智慧型 BIM 數據轉換樞紐

## ENV
windows 11 Home, 64 位元 <br/>
g [github link](https://github.com/voidint/g "版本切換器")  <br/>
```bash
$ ~/.g/bin/g.exe -v
g version 1.8.0
Built:         2025-07-11 13:24:15
Git branch:    master
Git commit:    a82e89cc
Go version:    go1.20.14
OS/Arch:       windows/amd64
Experimental:  false
```
Golang 
```bash
go1.25.10 windows/amd64 
```

### Tools
1. PowerShell ENV

terminate
```bash
... > code $PROFILE
# this command will open the default PowerShell configuration file using VSCode.
```
VsCode
```toml
$env:GOROOT="$HOME\.g\go"
$env:Path=-join("$HOME\.g\bin;", "$env:GOROOT\bin;", "$env:Path")
```

2. Git for Windows: Bash ENV

terminate
```bash
... > code ~/.bash_profile
# this command will open the default bash configuration file using VSCode.
```
VsCode
```toml
GOROOT="$HOME\.g\go"
PATH="$HOME\.g\bin:$GOROOT\bin:$PATH"
```

## Setup

### dependent
```bash
go mod init sftp-server
go get github.com/pkg/sftp
go get golang.org/x/crypto/ssh
go get github.com/spf13/viper
go mod tidy

```

### start
```bash
$ go run main.go --sftp-port=3022 --data-dir=/d/readyToConvert
----
2026/05/20 18:00:26 [API] 伺服器已啟動，監聽 Port: 8088
2026/05/20 18:00:26 [SFTP] 伺服器已啟動，監聽 Port: 3022, 儲存目錄: D:/readyToConvert
```