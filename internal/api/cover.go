package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/go-resty/resty/v2"
	"github.com/shamspias/fennec"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const maxRoomCoverBytes int64 = 2 * 1024 * 1024

const (
	webRoomCoverWidth   = 720
	webRoomCoverHeight  = 540
	webRoomCoverQuality = 95
)

const maxRoomCoverSourceBytes int64 = 64 * 1024 * 1024

// 先检查图片配置，避免恶意图片头部诱使解码器分配过大的像素缓冲区。
const maxRoomCoverSourceDimension = 16384

// 限制解码前的像素总数，避免恶意压缩图片占用过多内存。
const maxRoomCoverSourcePixels int64 = 64 * 1024 * 1024

// UploadRoomCover 使用 B 站 Web 图片接口上传本地图片，并返回更新封面接口使用的地址。
// 它与资料更新分开，以便在修改房间资料前先展示上传错误。
func (c *Client) UploadRoomCover(ctx context.Context, roomID, sessdata, biliJCT, filePath string) (string, error) {
	if strings.TrimSpace(sessdata) == "" || strings.TrimSpace(biliJCT) == "" {
		return "", fmt.Errorf("上传直播封面需要有效的 SESSDATA 和 bili_jct")
	}
	path := strings.TrimSpace(filePath)
	if path == "" {
		return "", fmt.Errorf("直播封面文件不能为空")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开直播封面失败: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("读取直播封面信息失败: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("直播封面路径必须是文件，不能是目录")
	}
	if info.Size() > maxRoomCoverSourceBytes {
		return "", fmt.Errorf("直播封面源文件不能超过 64 MB")
	}
	detectedMIME, err := mimetype.DetectFile(path)
	if err != nil {
		return "", fmt.Errorf("识别直播封面失败: %w", err)
	}
	if !isSupportedCoverMIME(detectedMIME) {
		return "", fmt.Errorf("直播封面实际格式不受支持：%s", detectedMIME.String())
	}
	uploadData, uploadType, width, height, err := normalizeCoverForUpload(ctx, file)
	if err != nil {
		return "", fmt.Errorf("处理直播封面失败: %w", err)
	}

	var body bytes.Buffer
	multipartWriter := multipart.NewWriter(&body)
	// Web 上传接口要求先提交 bucket、dir，再提交名为 file 的文件字段。
	if err := multipartWriter.WriteField("bucket", "live"); err != nil {
		return "", fmt.Errorf("准备直播封面上传失败: %w", err)
	}
	if err := multipartWriter.WriteField("dir", "new_room_cover"); err != nil {
		return "", fmt.Errorf("准备直播封面上传失败: %w", err)
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="blob"`)
	header.Set("Content-Type", uploadType)
	part, err := multipartWriter.CreatePart(header)
	if err != nil {
		return "", fmt.Errorf("准备直播封面上传失败: %w", err)
	}
	if _, err := part.Write(uploadData); err != nil {
		return "", fmt.Errorf("读取直播封面失败: %w", err)
	}
	if err := multipartWriter.Close(); err != nil {
		return "", fmt.Errorf("完成直播封面上传请求失败: %w", err)
	}

	pathURL, err := c.endpointByName("UploadRoomCover")
	if err != nil {
		return "", err
	}
	parsedURL, err := url.Parse(pathURL)
	if err != nil {
		return "", err
	}
	query := parsedURL.Query()
	query.Set("csrf", biliJCT)
	parsedURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsedURL.String(), bytes.NewReader(body.Bytes()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	req.Header.Set("Cookie", "SESSDATA="+sessdata+"; bili_jct="+biliJCT)
	req.Header.Set("User-Agent", biliBrowserUserAgent)
	req.Header.Set("Referer", "https://live.bilibili.com/")
	req.Header.Set("Origin", "https://live.bilibili.com")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		message := strings.TrimSpace(string(body))
		if message != "" {
			return "", fmt.Errorf("B 站封面上传接口返回 HTTP %d：%s", resp.StatusCode, message)
		}
		return "", fmt.Errorf("B 站封面上传接口返回 HTTP %d", resp.StatusCode)
	}
	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Data    struct {
			URL      string `json:"url"`
			Link     string `json:"link"`
			Location string `json:"location"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("解析直播封面上传响应失败: %w", err)
	}
	if result.Code != 0 {
		if result.Code == 42603 {
			if width > 0 && height > 0 {
				return "", fmt.Errorf("上传直播封面失败（错误码 %d）：B 站未接受处理后的图片尺寸（当前 %d×%d）", result.Code, width, height)
			}
			return "", fmt.Errorf("上传直播封面失败（错误码 %d）：B 站未接受处理后的图片尺寸", result.Code)
		}
		return "", fmt.Errorf("上传直播封面失败（错误码 %d）：%s", result.Code, responseMessage(result.Message, result.Msg))
	}
	coverURL := strings.TrimSpace(result.Data.URL)
	if coverURL == "" {
		coverURL = strings.TrimSpace(result.Data.Location)
	}
	if coverURL == "" {
		coverURL = strings.TrimSpace(result.Data.Link)
	}
	if coverURL == "" {
		return "", fmt.Errorf("直播封面上传接口未返回图片地址")
	}
	return coverURL, nil
}

func normalizeCoverForUpload(ctx context.Context, file *os.File) ([]byte, string, int, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", 0, 0, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, "", 0, 0, err
	}
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return nil, "", 0, 0, fmt.Errorf("请提供有效的 JPG、PNG 或 WebP 图片")
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxRoomCoverSourceDimension || config.Height > maxRoomCoverSourceDimension {
		return nil, "", 0, 0, fmt.Errorf("图片尺寸超出处理范围（最大 %d×%d）", maxRoomCoverSourceDimension, maxRoomCoverSourceDimension)
	}
	if int64(config.Width)*int64(config.Height) > maxRoomCoverSourcePixels {
		return nil, "", 0, 0, fmt.Errorf("图片像素数量超出处理范围")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, "", 0, 0, err
	}
	src, err := fennec.OpenAndOrient(file.Name())
	if err != nil {
		return nil, "", 0, 0, fmt.Errorf("请提供有效的 JPG、PNG 或 WebP 图片")
	}
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, "", 0, 0, fmt.Errorf("图片尺寸无效")
	}

	// B 站当前 Web 封面编辑器以画面中心裁出 4:3，再导出为
	// 720×540、质量 95 的 JPEG。APP 使用完整 4:3 封面，Web 展示时
	// 再从中间取 16:9，这里复现同一份上传素材。
	crop := centeredCropRect(src.Bounds(), webRoomCoverWidth, webRoomCoverHeight)
	dst := image.NewRGBA(image.Rect(0, 0, webRoomCoverWidth, webRoomCoverHeight))
	xdraw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, xdraw.Src)
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, xdraw.Over, nil)
	if err := ctx.Err(); err != nil {
		return nil, "", 0, 0, err
	}

	encoded, _, err := encodeJPEGWithinLimit(ctx, dst, maxRoomCoverBytes)
	if err != nil {
		return nil, "", 0, 0, err
	}
	return encoded, "image/jpeg", webRoomCoverWidth, webRoomCoverHeight, nil
}

// encodeJPEGWithinLimit 优先使用与 B 站网页编辑器相同的质量。
// 只有输出过大时才降低质量，并选取满足大小限制的最高质量。
func encodeJPEGWithinLimit(ctx context.Context, src image.Image, limit int64) ([]byte, int, error) {
	encode := func(quality int) ([]byte, error) {
		var encoded bytes.Buffer
		if err := jpeg.Encode(&encoded, src, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
		return encoded.Bytes(), nil
	}

	data, err := encode(webRoomCoverQuality)
	if err != nil {
		return nil, 0, err
	}
	if int64(len(data)) <= limit {
		return data, webRoomCoverQuality, nil
	}

	var best []byte
	bestQuality := 0
	low, high := 1, webRoomCoverQuality-1
	for low <= high {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		quality := low + (high-low)/2
		candidate, err := encode(quality)
		if err != nil {
			return nil, 0, err
		}
		if int64(len(candidate)) <= limit {
			best, bestQuality = candidate, quality
			low = quality + 1
		} else {
			high = quality - 1
		}
	}
	if best == nil {
		return nil, 0, fmt.Errorf("图片即使降低 JPEG 质量后仍超过 %d MB", limit/(1024*1024))
	}
	return best, bestQuality, nil
}

func centeredCropRect(bounds image.Rectangle, targetWidth, targetHeight int) image.Rectangle {
	width, height := bounds.Dx(), bounds.Dy()
	cropWidth, cropHeight := width, height
	if width*targetHeight > height*targetWidth {
		cropWidth = height * targetWidth / targetHeight
	} else if width*targetHeight < height*targetWidth {
		cropHeight = width * targetHeight / targetWidth
	}
	left := bounds.Min.X + (width-cropWidth)/2
	top := bounds.Min.Y + (height-cropHeight)/2
	return image.Rect(left, top, left+cropWidth, top+cropHeight)
}

// UploadRoomCoverURL 下载远程图片，再通过与本地文件相同的 B 站接口上传。
// 远程地址只作为图片来源，最终提交给直播间的是 B 站返回的图片地址。
func (c *Client) UploadRoomCoverURL(ctx context.Context, roomID, sessdata, biliJCT, imageURL string) (string, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(imageURL))
	if err != nil || (!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) || parsed.Host == "" {
		return "", fmt.Errorf("直播封面 URL 无效")
	}
	downloadHTTPClient := &http.Client{
		Transport:     c.HTTPClient.Transport,
		CheckRedirect: c.HTTPClient.CheckRedirect,
		Jar:           c.HTTPClient.Jar,
		Timeout:       30 * time.Second,
	}
	downloadClient := resty.NewWithClient(downloadHTTPClient).
		SetLogger(silentRestyLogger{}).
		SetRetryCount(2).
		SetRetryWaitTime(200 * time.Millisecond).
		SetRetryMaxWaitTime(time.Second).
		SetResponseBodyLimit(int(maxRoomCoverSourceBytes))
	resp, err := downloadClient.R().
		SetContext(ctx).
		SetHeaders(map[string]string{
			"Accept":     "image/jpeg,image/png,image/webp;q=0.9,*/*;q=0.1",
			"User-Agent": biliBrowserUserAgent,
		}).
		Get(parsed.String())
	if err != nil {
		if errors.Is(err, resty.ErrResponseBodyTooLarge) {
			return "", fmt.Errorf("直播封面源文件不能超过 64 MB")
		}
		return "", fmt.Errorf("下载直播封面失败: %w", err)
	}
	if !resp.IsSuccess() {
		return "", fmt.Errorf("下载直播封面失败：远程服务器返回 HTTP %d", resp.StatusCode())
	}
	data := resp.Body()
	ext, err := remoteCoverExtension(data, resp.Header().Get("Content-Type"))
	if err != nil {
		return "", err
	}
	temp, err := os.CreateTemp("", "bili-live-cover-*"+ext)
	if err != nil {
		return "", fmt.Errorf("准备远程直播封面失败: %w", err)
	}
	path := temp.Name()
	defer os.Remove(path)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return "", fmt.Errorf("保存远程直播封面失败: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("保存远程直播封面失败: %w", err)
	}
	return c.UploadRoomCover(ctx, roomID, sessdata, biliJCT, path)
}

func remoteCoverExtension(data []byte, declaredContentType string) (string, error) {
	detectedMIME := mimetype.Detect(data)
	detectedContentType := strings.ToLower(strings.TrimSpace(strings.Split(detectedMIME.String(), ";")[0]))
	if !isSupportedCoverMIME(detectedMIME) {
		return "", remoteCoverTypeError(declaredContentType, detectedContentType, false)
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", remoteCoverTypeError(declaredContentType, detectedContentType, true)
	}
	switch format {
	case "jpeg":
		return ".jpg", nil
	case "png":
		return ".png", nil
	case "webp":
		return ".webp", nil
	default:
		return "", fmt.Errorf("远程直播封面格式不受支持：%s", format)
	}
}

func isSupportedCoverMIME(detectedMIME *mimetype.MIME) bool {
	return detectedMIME.Is("image/jpeg") || detectedMIME.Is("image/png") || detectedMIME.Is("image/webp")
}

func remoteCoverTypeError(declaredContentType, detectedContentType string, damaged bool) error {
	declaredContentType = strings.TrimSpace(strings.Split(declaredContentType, ";")[0])
	if declaredContentType == "" {
		declaredContentType = "未提供"
	}
	if damaged {
		return fmt.Errorf("远程图片数据不完整或已损坏（响应类型：%s，检测类型：%s）", declaredContentType, detectedContentType)
	}
	return fmt.Errorf("远程服务器返回的不是 JPG、PNG 或 WebP 图片（响应类型：%s，检测类型：%s）", declaredContentType, detectedContentType)
}
