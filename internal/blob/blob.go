// Package blob 把圖片存進物件儲存。
//
// 用 MinIO 而非檔案系統，主要理由是管理性：content-addressed 的路徑
// （a3/f2/a3f2….jpg）用眼睛看不出是什麼，而 MinIO 的 console 可以直接
// 瀏覽與預覽。容量不是考量 —— 估算約 20GB/年，磁碟有 2.6TB。
package blob

import (
	"bytes"
	"context"
	"fmt"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Store struct {
	client *minio.Client
	bucket string
}

func Open(ctx context.Context, endpoint, user, password, bucket string) (*Store, error) {
	c, err := minio.New(endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(user, password, ""),
		// 同一個 docker network 內的內部流量，不需要 TLS
		Secure: false,
	})
	if err != nil {
		return nil, err
	}
	ok, err := c.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("連線 MinIO 失敗: %w", err)
	}
	if !ok {
		if err := c.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("建立 bucket 失敗: %w", err)
		}
	}
	return &Store{client: c, bucket: bucket}, nil
}

// Put 以內容雜湊為 key 寫入。同樣的內容寫第二次是無害的覆寫，
// 但先查存在可以省下一次上傳 —— 實測跨社團轉貼佔 39%。
func (s *Store) Put(ctx context.Context, key string, data []byte, contentType string) error {
	if _, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{}); err == nil {
		return nil // 已存在
	}
	_, err := s.client.PutObject(ctx, s.bucket, key,
		bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType})
	return err
}
