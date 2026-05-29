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
	// filesToUpload := []string{fragStorePath, compressionPath}
	// 增加可信賴度
	var filesToUpload []string
	if fragStorePath !=  "" {
		if _, err := os.Stat(fragStorePath); os.IsNotExist(err) {
			return fmt.Errorf("核心幾何檔案不存在: %s", fragStorePath)
		} else {
			filesToUpload = append(filesToUpload, fragStorePath)
		}
	}
	if compressionPath !=  "" {
		if _, err := os.Stat(compressionPath); os.IsNotExist(err) {
			utilities.Warn("[🚀 SeaweedFS S3] 材質壓縮包不存在: %s", compressionPath)
		} else {
			filesToUpload = append(filesToUpload, compressionPath)
		}
	}

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

	utilities.Info("[🚀 SeaweedFS S3] 已成功初始化 S3 用戶端，準備上傳檔案至 Bucket: %s", bucketName)
	utilities.Info("[🚀 SeaweedFS S3] 上傳檔案有： %s", strings.Join(filesToUpload, ", "))

	// 盡量嘗試把所有的檔案都上傳過，再決定是否為錯誤發生
	// 4. 依序檢查並上傳檔案
	var uploadErrors []string
	var isUploadFailed bool = false
	for _, targetPath := range filesToUpload {
		if targetPath == "" {
			continue
		}

		utilities.Debug("[🚀 SeaweedFS S3] 正在上傳檔案: %s 至 Bucket: %s\n", filepath.Base(targetPath), bucketName)

		// 執行 S3 上傳
		execS3UploadErr := executeS3Upload(ctx, uploader, bucketName, targetPath)
		if execS3UploadErr != nil {
			utilities.Warn("[🚀 SeaweedFS S3] 檔案 [%s] 透過 S3 上傳至 SeaweedFS 失敗: %v", filepath.Base(targetPath), execS3UploadErr)
			isUploadFailed = true
			uploadErrors = append(uploadErrors, fmt.Sprintf("%s: %v", filepath.Base(targetPath), execS3UploadErr))
		} else {
			utilities.Info("[🚀 SeaweedFS S3] 檔案 [%s] 已成功上傳至 SeaweedFS Bucket: %s", filepath.Base(targetPath), bucketName)
		}
	}

	if isUploadFailed {
		return fmt.Errorf("%v", strings.Join(uploadErrors, ", "))
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