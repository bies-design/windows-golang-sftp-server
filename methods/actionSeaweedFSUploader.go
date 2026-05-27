package methods

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/viper"

	"intelligent-bim-data-conversion-hub/utilities"
)

// UploadToSeaweedFS 負責將特定命名下的 .frag 與材質壓縮包透過 S3 介面上傳至 SeaweedFS
func UploadToSeaweedFS(fragStorePath string, compressionPath string) error {
	// 1. 驗證與收集檔案路徑
	filesToUpload := []string{fragStorePath, compressionPath}

	// 2. 讀取配置並決定 S3 Endpoint
	s3URL := viper.GetString("S3_Cloud_URL")
	if s3URL == "" {
		s3URL = viper.GetString("SeaweedFS_S3_URL")
	}
	if s3URL == "" {
		s3URL = viper.GetString("MinIO_URL")
	}
	if s3URL == "" {
		// 預設指向 Docker 暴露出來的 seaweedfs-s3 埠口
		s3URL = "http://100.103.58.19:8333"
	} else {
		s3URL = strings.TrimRight(s3URL, "/")
	}

	// 寫死或由 viper 讀取 s3_config.json 內的憑證
	accessKey := viper.GetString("SeaweedFS_Access_Key")
	if accessKey == "" {
		accessKey = "seaWeedFSwbMG1U93PWstE8f"
	}
	secretKey := viper.GetString("SeaweedFS_Secret_Key")
	if secretKey == "" {
		secretKey = "PGYlGY04300vYN92D8KOfakT"
	}
	bucketName := viper.GetString("SeaweedFS_Bucket_Name")// 目標 Bucket
	if bucketName == "" {
		bucketName = "public-assets"
	}

	// 3. 初始化 AWS S3 用戶端（對接 SeaweedFS S3 介面）
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-1"), // SeaweedFS 不需要特定 Region，但 SDK 要求必填，給預設即可
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
		config.WithEndpointResolverWithOptions(aws.EndpointResolverWithOptionsFunc(
			func(service, region string, options ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{
					URL:               s3URL,
					HostnameImmutable: true, // 必須為 true，防止 SDK 自動拼接 bucket.s3.amazonaws.com
				}, nil
			})),
	)
	if err != nil {
		return fmt.Errorf("初始化 S3 配置失敗: %v", err)
	}

	s3Client := s3.NewFromConfig(cfg)
	// 使用 Uploader 可以支援大檔案自動分塊串流上傳，不爆記憶體
	uploader := manager.NewUploader(s3Client)

	// 4. 依序檢查並上傳檔案
	for _, targetPath := range filesToUpload {
		if targetPath == "" {
			continue
		}

		// 防呆與過濾材質包不存在的狀況
		if _, err := os.Stat(targetPath); os.IsNotExist(err) {
			ext := filepath.Ext(targetPath)
			// 檢查是否為常見的壓縮格式
			if ext == ".bzip2" || ext == ".gzip" || ext == ".zip" || strings.HasSuffix(targetPath, ".tar.gz") || strings.HasSuffix(targetPath, ".tar.bzip2") {
				utilities.Info("[提示] 找不到材質包 %s，此模型應為純幾何，跳過上傳。\n", filepath.Base(targetPath))
				continue
			}
			return fmt.Errorf("上傳失敗：核心幾何檔案不存在: %s", targetPath)
		}

		utilities.Debug("[🚀 SeaweedFS S3] 正在上傳檔案: %s 至 Bucket: %s\n", filepath.Base(targetPath), bucketName)

		// 執行 S3 上傳
		err = executeS3Upload(ctx, uploader, bucketName, targetPath)
		if err != nil {
			return fmt.Errorf("檔案 [%s] 透過 S3 上傳至 SeaweedFS 失敗: %v", filepath.Base(targetPath), err)
		}
	}

	return nil
}

// 內部輔助函式：使用 S3 串流上傳檔案
func executeS3Upload(ctx context.Context, uploader *manager.Uploader, bucket, localFilePath string) error {
	file, err := os.Open(localFilePath)
	if err != nil {
		return fmt.Errorf("無法開啟本地檔案: %v", err)
	}
	defer file.Close()

	filename := filepath.Base(localFilePath)

	// 執行上傳（SDK 自動處理 Chunked 與 記憶體控管）
	_, err = uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(filename),
		Body:   file, // 直接傳入 os.File 指針，享用低記憶體零拷貝串流
	})
	if err != nil {
		return err
	}

	return nil
}