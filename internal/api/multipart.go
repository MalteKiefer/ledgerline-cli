package api

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

// uploadMultipart POSTs a streamed multipart/form-data body to path: the optional
// string fields, then one file part (fieldName) whose bytes come from open. The
// body is streamed through an io.Pipe so a large file is never buffered whole.
//
// open must return a FRESH reader each call so the request can be replayed across
// the transport's retry/backoff (a bursty parallel upload periodically 429s or
// hits a struggling gateway). The returned status code lets callers distinguish
// 200 (duplicate) from 201 (created). A 2xx body, if any, is decoded into out.
func (c *Client) uploadMultipart(ctx context.Context, path, fieldName, fileName string, fields map[string]string, open func() (io.ReadCloser, error), out any) (int, error) {
	var status int
	resp, err := c.retriableDo(ctx, func() (*http.Request, error) {
		src, err := open()
		if err != nil {
			return nil, err
		}
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		go func() {
			defer src.Close()
			for k, v := range fields {
				if err := mw.WriteField(k, v); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
			}
			part, err := mw.CreateFormFile(fieldName, fileName)
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			if _, err := io.Copy(part, src); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			if err := mw.Close(); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			_ = pw.Close()
		}()

		req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint(path), pr)
		if err != nil {
			return nil, err
		}
		c.applyAuth(req)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		return req, nil
	})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	return status, readJSON(resp, out, 8<<20)
}

// getStream GETs path and copies the response body to w, bypassing JSON decoding.
// Used for raw blob/photo/file downloads. Non-2xx becomes an *APIError.
func (c *Client) getStream(ctx context.Context, path string, w io.Writer) error {
	resp, err := c.retriableDo(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", c.endpoint(path), nil)
		if err != nil {
			return nil, err
		}
		c.applyAuth(req)
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	return nil
}
