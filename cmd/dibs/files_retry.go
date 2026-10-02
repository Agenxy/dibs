package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type retryableTransferError struct{ error }

func (e retryableTransferError) Unwrap() error { return e.error }

func uploadFile(
	file *os.File, size int64, digest string, descriptor fileDescriptor, args map[string]any,
) (fileResult, error) {
	client := byteClient()
	defer client.CloseIdleConnections()
	var offset int64
	for attempt := range 5 {
		transferBackoff(attempt)
		result, err := uploadAttempt(client, file, size, offset, digest, descriptor)
		if err == nil {
			return result, nil
		}
		var retry retryableTransferError
		if !errors.As(err, &retry) {
			return fileResult{}, err
		}
		descriptor, offset, err = resumeUpload(client, descriptor, args)
		if err != nil {
			return fileResult{}, err
		}
		if offset < 0 || offset > size {
			return fileResult{}, errors.New("server upload offset outside source bounds")
		}
	}
	return fileResult{}, errors.New("upload incomplete after five bounded attempts; rerun dibs put")
}

func transferBackoff(attempt int) {
	if attempt > 0 {
		time.Sleep(time.Duration(attempt*attempt) * 100 * time.Millisecond)
	}
}

func uploadAttempt(
	client *http.Client, file *os.File, size, offset int64, digest string, descriptor fileDescriptor,
) (fileResult, error) {
	req, err := http.NewRequest(http.MethodPatch, descriptor.URL, io.NewSectionReader(file, offset, size-offset))
	if err != nil {
		return fileResult{}, errors.New("invalid upload descriptor")
	}
	req.ContentLength = size - offset
	if size == offset {
		req.Body = http.NoBody
	}
	req.Header.Set("Content-Type", "application/partial-upload")
	req.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	req.Header.Set("Upload-Complete", "?1")
	req.Header.Set("Upload-Draft-Interop-Version", "9")
	response, err := requestBytes(client, req)
	if err != nil {
		return fileResult{}, retryableTransferError{err}
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case 200:
		var result fileResult
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
			return fileResult{}, retryableTransferError{err}
		}
		if result.Blob != "sha256:"+digest || result.Size != size {
			return fileResult{}, errors.New("upload result differs from source digest or size")
		}
		return result, nil
	case 409, 410, 503:
		return fileResult{}, retryableTransferError{errors.New("upload needs offset recovery")}
	default:
		return fileResult{}, fmt.Errorf("upload refused (HTTP %d); recheck access, size and source hash", response.StatusCode)
	}
}

var errUploadGone = errors.New("upload ticket expired or daemon restarted")

func resumeUpload(client *http.Client, descriptor fileDescriptor, args map[string]any) (fileDescriptor, int64, error) {
	offset, err := probeUpload(client, descriptor)
	if errors.Is(err, errUploadGone) {
		descriptor, err = authorizeFile("upload", args)
		if err == nil {
			offset, err = probeUpload(client, descriptor)
		}
	}
	return descriptor, offset, err
}

func probeUpload(client *http.Client, descriptor fileDescriptor) (int64, error) {
	req, err := http.NewRequest(http.MethodHead, descriptor.URL, nil)
	if err != nil {
		return 0, errors.New("invalid upload descriptor")
	}
	response, err := requestBytes(client, req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == 410 {
		return 0, errUploadGone
	}
	if response.StatusCode != 204 {
		return 0, fmt.Errorf("upload offset query refused (HTTP %d)", response.StatusCode)
	}
	return strconv.ParseInt(response.Header.Get("Upload-Offset"), 10, 64)
}

func downloadFile(file *os.File, blob string, descriptor fileDescriptor) (int64, error) {
	client := byteClient()
	defer client.CloseIdleConnections()
	for attempt := range 5 {
		transferBackoff(attempt)
		if err := downloadAttempt(client, file, blob, descriptor); err != nil {
			var retry retryableTransferError
			if errors.As(err, &retry) {
				continue
			}
			return 0, err
		}
		return verifyDownload(file, blob)
	}
	return 0, errors.New("download incomplete after five bounded attempts; destination was not published")
}

func downloadAttempt(client *http.Client, file *os.File, blob string, descriptor fileDescriptor) error {
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, descriptor.URL, nil)
	if err != nil {
		return errors.New("invalid download descriptor")
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		req.Header.Set("If-Range", `"`+blob+`"`)
	}
	response, err := requestBytes(client, req)
	if err != nil {
		return retryableTransferError{err}
	}
	defer func() { _ = response.Body.Close() }()
	return appendDownload(file, blob, offset, response)
}

func verifyDownload(file *os.File, blob string) (int64, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return 0, err
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != blob {
		return 0, errors.New("download hash mismatch; destination was not published")
	}
	return size, nil
}

func appendDownload(file *os.File, blob string, offset int64, response *http.Response) error {
	if response.StatusCode == 416 && offset > 0 {
		return nil
	} // digest verification is still required
	if response.StatusCode != 200 && response.StatusCode != 206 {
		return fmt.Errorf("download refused (HTTP %d)", response.StatusCode)
	}
	if response.Header.Get("ETag") != `"`+blob+`"` {
		return errors.New("download ETag does not identify requested content")
	}
	if response.StatusCode == 206 {
		if !strings.HasPrefix(response.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-", offset)) {
			return errors.New("server returned a different byte range")
		}
	} else if offset > 0 {
		if err := file.Truncate(0); err != nil {
			return err
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	_, err := io.Copy(file, response.Body)
	if err != nil {
		var disk *os.PathError
		if errors.As(err, &disk) {
			return err
		}
		return retryableTransferError{err} // retained bytes are verified after resumed download
	}
	return nil
}
